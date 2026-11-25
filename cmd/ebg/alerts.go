package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/alert"
	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func runAlerts(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("alerts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sloPath := fs.String("slo", "configs/slos.yaml", "SLO definition file")
	policyPath := fs.String("alerts", "configs/alerts.yaml", "alert policy file")
	eventsPath := fs.String("events", "", "events file written by `ebg parse` (JSON lines), or - for stdin (required)")
	nowArg := fs.String("now", "", "evaluation time (RFC3339); default is the newest event, so a replay always gives the same answer")
	step := fs.Duration("step", 0, "also replay the alerts over time at this step and print the episodes")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitError
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "ebg alerts:", err)
		return exitError
	}
	if fs.NArg() != 0 {
		return fail(errors.New("unexpected arguments; use --events FILE"))
	}
	if *eventsPath == "" {
		return fail(errors.New("--events is required (a file written by `ebg parse`, or - for stdin)"))
	}
	if *step < 0 {
		return fail(errors.New("--step must not be negative"))
	}
	f, err := slo.Load(*sloPath)
	if err != nil {
		return fail(err)
	}
	pol, err := alert.Load(*policyPath)
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

	var now time.Time
	if *nowArg != "" {
		now, err = time.Parse(time.RFC3339, *nowArg)
		if err != nil {
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

	fmt.Fprintf(stdout, "alerts at %s (%d events)\n", ts(now), len(evs))
	page, ticket, unsure := 0, 0, 0
	for _, s := range f.SLOs {
		fmt.Fprintf(stdout, "slo %s (objective %g%%, window %s, scale=%.6g)\n", s.Name, s.Objective, s.Window.Std(), pol.Scale(s.Window.Std()))
		for _, a := range alert.Evaluate(pol, s, evs, now) {
			state := "ok"
			if a.Insufficient {
				state = "insufficient"
				unsure++
			}
			if a.Firing {
				state = "FIRING"
				if a.Severity == alert.SeverityPage {
					page++
				} else {
					ticket++
				}
			}
			fmt.Fprintf(stdout, "  %-12s %-6s %-12s %s\n", state, a.Severity, a.Rule, a.Explain())
		}
	}
	fmt.Fprintf(stdout, "firing: %d page, %d ticket, %d insufficient\n", page, ticket, unsure)

	if *step > 0 && len(evs) > 0 {
		from := evs[0].Timestamp
		for _, e := range evs {
			if e.Timestamp.Before(from) {
				from = e.Timestamp
			}
		}
		from = from.UTC()
		if from.After(now) {
			from = now
		}
		eps, err := alert.AllEpisodes(pol, f.SLOs, evs, from, now, *step)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "episodes (replayed every %s from %s):\n", *step, ts(from))
		if len(eps) == 0 {
			fmt.Fprintln(stdout, "  none")
		}
		for _, e := range eps {
			state := "ended " + ts(e.End)
			if e.Open {
				state = "still firing"
			}
			fmt.Fprintf(stdout, "  %-6s %-22s %-12s start %s, %s, peak %.1fx\n", e.Severity, e.SLO, e.Rule, ts(e.Start), state, e.PeakBurn)
		}
	}
	return exitOK
}
