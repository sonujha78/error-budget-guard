package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/clean"
	"github.com/sonujha78/error-budget-guard/internal/event"
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
	fmt.Fprintln(w, "  ebg parse --format nginx|json|probe|prom [--service NAME] [--slo FILE]")
	fmt.Fprintln(w, "            [--skew SRC=+3s,...] [--interval 10s] [--dedupe-lines] <file|->")
	fmt.Fprintln(w, "  ebg alerts --events FILE|- [--slo FILE] [--alerts FILE] [--now RFC3339] [--step 10s]")
	fmt.Fprintln(w, "  ebg gate   --events FILE|- [--policy FILE] [--override REASON --override-by NAME]   exit 0 allow, 1 warn, 2 block, 3 error")
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
	case "alerts":
		return runAlerts(args[1:], stdin, stdout, stderr)
	case "gate":
		return runGate(args[1:], stdin, stdout, stderr)
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
	service := fs.String("service", "", "service name (required for nginx and prom, default for json and probe)")
	skewArg := fs.String("skew", "", "clock offsets, like nginx-log=+3s,probe=-2s (positive = that source's clock is ahead)")
	interval := fs.Duration("interval", 0, "expected time between samples; turns on the coverage report")
	dedupeLines := fs.Bool("dedupe-lines", false, "also drop identical lines that have no request id (can hide real traffic)")
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
	sk, err := clean.ParseSkew(*skewArg)
	if err != nil {
		fmt.Fprintln(stderr, "ebg parse:", err)
		return exitError
	}
	if *interval < 0 {
		fmt.Fprintln(stderr, "ebg parse: --interval must not be negative")
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
	res, err := sources.CollectOpts(in, *format, *service, f.SLOs, sources.Options{DedupeLines: *dedupeLines})
	if err != nil {
		fmt.Fprintln(stderr, "ebg parse:", err)
		return exitError
	}

	evs, unused := clean.ApplySkew(res.Events, sk)
	event.Sort(evs)
	enc := json.NewEncoder(stdout)
	for _, e := range evs {
		if err := enc.Encode(e); err != nil {
			fmt.Fprintln(stderr, "ebg parse:", err)
			return exitError
		}
	}

	st := res.Stats
	fmt.Fprintf(stderr, "parsed: lines=%d blank=%d malformed=%d requests=%d events=%d no_latency=%d duplicates=%d out_of_order=%d\n",
		st.Lines, st.Blank, st.Malformed, st.Requests, st.Events, st.NoLatency, st.Duplicates, st.OutOfOrder)
	if st.Scrapes > 0 {
		fmt.Fprintf(stderr, "prometheus: scrapes=%d resets=%d new_series=%d clamped=%d\n",
			st.Scrapes, st.Resets, st.NewSeries, st.Clamped)
	}
	for _, w := range st.Warnings {
		fmt.Fprintln(stderr, "  warning: "+w)
	}
	for _, name := range unused {
		fmt.Fprintf(stderr, "  warning: skew for source %q matched no events\n", name)
	}
	if *interval > 0 {
		printCoverage(stderr, *format, res.Times, sk[sources.SourceName(*format)], *interval)
	}
	for _, m := range st.Errors {
		fmt.Fprintln(stderr, "  "+m)
	}
	return exitOK
}

// printCoverage measures gaps over the observed span only: from one interval before the
// first sample to the last sample. Gaps before or after that cannot be known from the data.
func printCoverage(w io.Writer, format string, times []time.Time, off, interval time.Duration) {
	label := "coverage"
	if format != sources.FormatProbe && format != sources.FormatProm {
		label = "silence"
	}
	if len(times) == 0 {
		fmt.Fprintf(w, "%s: no samples, so it cannot be measured\n", label)
		return
	}
	shifted := make([]time.Time, len(times))
	first, last := times[0].Add(-off), times[0].Add(-off)
	for i, t := range times {
		t = t.Add(-off)
		shifted[i] = t
		if t.Before(first) {
			first = t
		}
		if t.After(last) {
			last = t
		}
	}
	cov, err := clean.MeasureCoverage(shifted, first.Add(-interval), last, interval)
	if err != nil {
		fmt.Fprintf(w, "%s: %v\n", label, err)
		return
	}
	fmt.Fprintf(w, "%s: samples=%d span=%s missing=%s ratio=%.3f gaps=%d\n",
		label, cov.Samples, cov.Span, cov.Missing, cov.Ratio, len(cov.Gaps))
	for i, g := range cov.Gaps {
		if i == 10 {
			fmt.Fprintf(w, "  ... %d more gaps\n", len(cov.Gaps)-10)
			break
		}
		fmt.Fprintf(w, "  gap: %s to %s\n", g.Start.Format(time.RFC3339), g.End.Format(time.RFC3339))
	}
	if label == "silence" {
		fmt.Fprintln(w, "  note: access logs cannot tell a quiet period from lost data")
	}
	fmt.Fprintln(w, "  note: only the observed span is measured; gaps before the first or after the last sample are unknown")
}
