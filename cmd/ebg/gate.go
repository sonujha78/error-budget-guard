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
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

func budgetLine(s budget.Status) string {
	if s.Insufficient || !s.HasData {
		return "insufficient data: " + s.Reason + " (budget numbers not shown)"
	}
	rem := 0.0
	if s.BudgetTotal > 0 {
		rem = s.BudgetRemaining / s.BudgetTotal * 100
	}
	return fmt.Sprintf("SLI %.3f%% (objective %g%%), used %.1f of %.1f events, %.1f%% of the budget remains",
		s.SLI*100, s.Objective, s.BudgetUsed, s.BudgetTotal, rem)
}

// runGate returns 0 allow, 1 warn, 2 block, 3 tool error. Every flag problem, help included,
// is 3, so a script can never mistake it for "allow".
func runGate(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sloPath := fs.String("slo", "configs/slos.yaml", "SLO definition file")
	alertPath := fs.String("alerts", "configs/alerts.yaml", "alert policy file")
	policyPath := fs.String("policy", "configs/policy.yaml", "error budget policy file")
	eventsPath := fs.String("events", "", "events file written by `ebg parse` (JSON lines), or - for stdin (required)")
	nowArg := fs.String("now", "", "evaluation time (RFC3339); default is the newest event")
	override := fs.String("override", "", "let the release go ahead anyway; the reason appears in the output")
	overrideBy := fs.String("override-by", "", "who overrides (required with --override)")
	divisor := fs.Int("forecast-divisor", forecast.DefaultDivisor, "the forecast burn is measured over SLO window / this")
	fail := func(err error) int {
		fmt.Fprintln(stderr, "ebg gate:", err)
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
	if *divisor <= 0 {
		return fail(errors.New("--forecast-divisor must be positive"))
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

	fmt.Fprintf(stdout, "gate at %s (%d events)\n", ts(now), len(evs))
	for _, st := range states {
		s := st.Status
		fmt.Fprintf(stdout, "slo %s (objective %g%%, window %s)\n", s.SLO, s.Objective, s.Window)
		fmt.Fprintln(stdout, "  budget:   "+budgetLine(s))
		fmt.Fprintln(stdout, "  forecast: "+st.Forecast.Explain())
		var names []string
		unsure := 0
		for _, a := range st.Alerts {
			if a.Firing {
				names = append(names, a.Rule+" ("+a.Severity+")")
			}
			if a.Insufficient {
				unsure++
			}
		}
		line := "none firing"
		if len(names) > 0 {
			line = "FIRING " + strings.Join(names, ", ")
		}
		fmt.Fprintf(stdout, "  alerts:   %s, %d rules with insufficient data\n", line, unsure)
	}
	fmt.Fprintln(stdout, "reasons:")
	for _, r := range d.Reasons {
		fmt.Fprintln(stdout, "  "+r.String())
	}
	fmt.Fprintf(stdout, "decision: %s (exit %d)\n", d.Summary(), d.ExitCode())
	return d.ExitCode()
}
