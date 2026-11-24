package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sonujha78/error-budget-guard/internal/slo"
	"github.com/sonujha78/error-budget-guard/internal/sources"
)

// version is overridden at build time with -ldflags.
var version = "0.0.1-dev"

const (
	exitOK = 0
	// exitError is a tool error. 0, 1 and 2 are reserved for the release gate (allow, warn, block),
	// so a broken run can never be mistaken for "allow".
	exitError = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "ebg: Error Budget Guard")
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  ebg version")
	fmt.Fprintln(w, "  ebg parse --format nginx|json|probe|prom [--service NAME] [--slo FILE] <file|->")
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitError
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, "ebg", version)
		return exitOK
	case "help", "--help", "-h":
		usage(stdout)
		return exitOK
	case "parse":
		return runParse(args[1:], stdin, stdout, stderr)
	}
	fmt.Fprintf(stderr, "ebg: unknown command %q\n", args[0])
	usage(stderr)
	return exitError
}

func runParse(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("parse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sloPath := fs.String("slo", "configs/slos.yaml", "SLO definition file")
	format := fs.String("format", "", "input format: nginx, json, probe or prom (required)")
	service := fs.String("service", "", "service name (required for nginx, default for json and probe)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitError
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "ebg parse: need exactly one input file (or - for stdin); put flags before the file")
		return exitError
	}
	if *format == "" {
		fmt.Fprintln(stderr, "ebg parse: --format is required")
		return exitError
	}
	f, err := slo.Load(*sloPath)
	if err != nil {
		fmt.Fprintln(stderr, "ebg parse:", err)
		return exitError
	}
	in := stdin
	if fs.Arg(0) != "-" {
		fh, err := os.Open(fs.Arg(0))
		if err != nil {
			fmt.Fprintln(stderr, "ebg parse:", err)
			return exitError
		}
		defer fh.Close()
		in = fh
	}
	res, err := sources.Collect(in, *format, *service, f.SLOs)
	if err != nil {
		fmt.Fprintln(stderr, "ebg parse:", err)
		return exitError
	}
	enc := json.NewEncoder(stdout)
	for _, e := range res.Events {
		if err := enc.Encode(e); err != nil {
			fmt.Fprintln(stderr, "ebg parse:", err)
			return exitError
		}
	}
	st := res.Stats
	fmt.Fprintf(stderr, "parsed: lines=%d blank=%d malformed=%d requests=%d events=%d no_latency=%d\n",
		st.Lines, st.Blank, st.Malformed, st.Requests, st.Events, st.NoLatency)
	if st.Scrapes > 0 {
		fmt.Fprintf(stderr, "prometheus: scrapes=%d resets=%d new_series=%d duplicates=%d out_of_order=%d clamped=%d\n",
			st.Scrapes, st.Resets, st.NewSeries, st.Duplicates, st.OutOfOrder, st.Clamped)
	}
	for _, w := range st.Warnings {
		fmt.Fprintln(stderr, "  warning: "+w)
	}
	for _, m := range st.Errors {
		fmt.Fprintln(stderr, "  "+m)
	}
	return exitOK
}
