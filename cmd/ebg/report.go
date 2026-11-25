package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/alert"
	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/forecast"
	"github.com/sonujha78/error-budget-guard/internal/gate"
	"github.com/sonujha78/error-budget-guard/internal/redact"
	"github.com/sonujha78/error-budget-guard/internal/report"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

// runReport writes the Markdown report. It describes the decision, it does not enforce it:
// the exit code is 0, or 3 for a tool error. Use `ebg gate` for the exit code of the decision.
func runReport(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sloPath := fs.String("slo", "configs/slos.yaml", "SLO definition file")
	alertPath := fs.String("alerts", "configs/alerts.yaml", "alert policy file")
	policyPath := fs.String("policy", "configs/policy.yaml", "error budget policy file")
	eventsPath := fs.String("events", "", "events file written by `ebg parse` (JSON lines), or - for stdin (required)")
	nowArg := fs.String("now", "", "evaluation time (RFC3339); default is the newest event")
	override := fs.String("override", "", "override reason (shown in the report)")
	overrideBy := fs.String("override-by", "", "who overrides (required with --override)")
	divisor := fs.Int("forecast-divisor", forecast.DefaultDivisor, "the forecast burn is measured over SLO window / this")
	qualityArg := fs.String("quality", "", "comma separated statistics files written by `ebg parse --stats-out`")
	outPath := fs.String("out", "", "write the report to this file instead of stdout")
	title := fs.String("title", "", "report title")
	top := fs.Int("top", 5, "how many error sources to list")
	fail := func(err error) int {
		fmt.Fprintln(stderr, "ebg report:", err)
		return exitError
	}
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() != 0 {
		return fail(errors.New("unexpected arguments; use --events FILE"))
	}
	if *eventsPath == "" {
		return fail(errors.New("--events is required (a file written by `ebg parse`, or - for stdin)"))
	}
	if *divisor <= 0 || *top <= 0 {
		return fail(errors.New("--forecast-divisor and --top must be positive"))
	}
	if (*override == "") != (*overrideBy == "") {
		return fail(errors.New("--override and --override-by must be given together"))
	}
	f, err := slo.Load(*sloPath)
	if err != nil {
		return fail(err)
	}
	ap, err := alert.Load(*alertPath)
	if err != nil {
		return fail(err)
	}
	pol, err := gate.Load(*policyPath)
	if err != nil {
		return fail(err)
	}
	in := stdin
	if *eventsPath != "-" {
		fh, err := os.Open(*eventsPath)
		if err != nil {
			return fail(err)
		}
		defer fh.Close()
		in = fh
	}
	evs, err := event.ReadJSONL(in)
	if err != nil {
		return fail(err)
	}
	var quality []report.Quality
	for _, p := range strings.Split(*qualityArg, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fh, err := os.Open(p)
		if err != nil {
			return fail(err)
		}
		q, err := report.ReadQuality(fh)
		fh.Close()
		if err != nil {
			return fail(fmt.Errorf("quality file %q: %v", p, err))
		}
		quality = append(quality, q)
	}

	var now time.Time
	if *nowArg != "" {
		if now, err = time.Parse(time.RFC3339, *nowArg); err != nil {
			return fail(errors.New("--now must be RFC3339, like 2026-11-24T10:00:00Z"))
		}
	} else {
		if len(evs) == 0 {
			return fail(errors.New("no events, so there is no evaluation time: pass --now"))
		}
		for _, e := range evs {
			if e.Timestamp.After(now) {
				now = e.Timestamp
			}
		}
	}
	now = now.UTC()

	states := make([]gate.SLOState, 0, len(f.SLOs))
	for _, s := range f.SLOs {
		st := budget.ComputeWith(s, evs, now, pol.MinHistoryCoverage)
		states = append(states, gate.SLOState{
			Status:   st,
			Alerts:   alert.Evaluate(ap, s, evs, now),
			Forecast: forecast.Compute(s, evs, now, st, *divisor),
		})
	}
	d := gate.Decide(pol, states)
	if *override != "" {
		if d, err = d.WithOverride(redact.Scrub(*override), redact.Scrub(*overrideBy)); err != nil {
			return fail(err)
		}
	}
	md, n := report.Build(report.Input{
		Title: redact.Scrub(*title), Now: now, Events: evs, States: states,
		Decision: d, Quality: quality, TopN: *top,
	})
	if *outPath != "" {
		if err := os.WriteFile(*outPath, []byte(md), 0o644); err != nil {
			return fail(err)
		}
	} else {
		fmt.Fprint(stdout, md)
	}
	if n > 0 {
		fmt.Fprintf(stderr, "note: %d values were redacted from the report\n", n)
	}
	return exitOK
}
