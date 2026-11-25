// Package report writes the Markdown budget report: numbers a person can check.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/gate"
	"github.com/sonujha78/error-budget-guard/internal/redact"
)

// Input is everything the report shows. It holds no wall-clock time, so a replay of the same
// data gives the same report.
type Input struct {
	Title    string
	Now      time.Time
	Events   []event.Event
	States   []gate.SLOState
	Decision gate.Decision
	Quality  []Quality
	TopN     int
}

// ErrorSource is a number of bad events for one endpoint or status class.
type ErrorSource struct {
	Key string
	Bad int64
}

// Errors are the bad events of one SLO, grouped. TotalBad counts all of them, not only the top N.
type Errors struct {
	TotalBad   int64
	ByEndpoint []ErrorSource
	ByClass    []ErrorSource
}

func ranked(m map[string]int64, n int) []ErrorSource {
	out := make([]ErrorSource, 0, len(m))
	for k, v := range m {
		out = append(out, ErrorSource{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bad != out[j].Bad {
			return out[i].Bad > out[j].Bad
		}
		return out[i].Key < out[j].Key
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// TopErrors groups the bad events (valid - good) of one service and SLI in (from, to] by
// endpoint and by status class, largest first. Ties are sorted by name.
func TopErrors(evs []event.Event, service, sli string, from, to time.Time, n int) Errors {
	var e Errors
	ep, cl := map[string]int64{}, map[string]int64{}
	for _, v := range evs {
		if v.Service != service || v.SLI != sli || !v.Timestamp.After(from) || v.Timestamp.After(to) {
			continue
		}
		bad := v.Valid - v.Good
		if bad <= 0 {
			continue
		}
		endpoint, class := event.SplitLabel(v.Label)
		if class == "" {
			class = "unknown"
		}
		ep[endpoint] += bad
		cl[class] += bad
		e.TotalBad += bad
	}
	e.ByEndpoint, e.ByClass = ranked(ep, n), ranked(cl, n)
	return e
}

func cell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

func writeTable(b *strings.Builder, head string, rows []ErrorSource, total int64) {
	fmt.Fprintf(b, "| %s | Bad events | Share of bad events |\n|---|---:|---:|\n", head)
	for _, r := range rows {
		fmt.Fprintf(b, "| %s | %d | %.1f%% |\n", cell(r.Key), r.Bad, float64(r.Bad)/float64(total)*100)
	}
	b.WriteString("\n")
}

// Build writes the report and returns it with the number of values that had to be redacted.
func Build(in Input) (string, int) {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	title := in.Title
	if title == "" {
		title = "Error budget report"
	}
	topN := in.TopN
	if topN <= 0 {
		topN = 5
	}
	d := in.Decision

	p("# %s\n\n", title)
	p("- Evaluated at: `%s` (the newest event unless `--now` was given; no wall-clock time is used)\n", in.Now.UTC().Format(time.RFC3339))
	p("- Events read: %d\n", len(in.Events))
	p("- Gate decision: **%s** (exit %d)\n\n", strings.ToUpper(d.Effective), d.ExitCode())

	p("## Gate decision\n\n**%s** (exit %d)\n\n", d.Summary(), d.ExitCode())
	if d.Overridden {
		p("> **Override:** %s bypassed a %s decision. Reason: %s\n\n", d.OverrideBy, strings.ToUpper(d.Level), d.OverrideReason)
	}
	p("Reasons:\n\n")
	for _, r := range d.Reasons {
		p("- %s\n", r.String())
	}
	p("\n")

	p("## SLO status\n\n| SLO | Objective | SLI | Budget used | Budget remaining | Status |\n|---|---:|---:|---:|---:|---|\n")
	var notes []string
	for _, st := range in.States {
		s := st.Status
		if s.Insufficient || !s.HasData {
			p("| %s | %g%% | n/a | n/a | n/a | insufficient data |\n", cell(s.SLO), s.Objective)
			notes = append(notes, fmt.Sprintf("- %s: %s", s.SLO, s.Reason))
			continue
		}
		used, rem := 0.0, 0.0
		if s.BudgetTotal > 0 {
			used, rem = s.BudgetUsed/s.BudgetTotal*100, s.BudgetRemaining/s.BudgetTotal*100
		}
		status := "within budget"
		if s.Overspent {
			status = "overspent"
		}
		p("| %s | %g%% | %.3f%% | %.1f of %.1f events (%.1f%%) | %.1f%% | %s |\n",
			cell(s.SLO), s.Objective, s.SLI*100, s.BudgetUsed, s.BudgetTotal, used, rem, status)
	}
	p("\n")
	if len(notes) > 0 {
		p("Why some numbers are missing:\n\n%s\n\n", strings.Join(notes, "\n"))
	}

	p("## Active alerts\n\n")
	var firing, unsure []string
	for _, st := range in.States {
		for _, a := range st.Alerts {
			switch {
			case a.Firing:
				firing = append(firing, fmt.Sprintf("| %s | %s | %s | %s |", cell(a.SLO), a.Rule, a.Severity, cell(a.Explain())))
			case a.Insufficient:
				unsure = append(unsure, fmt.Sprintf("- %s / %s: %s", a.SLO, a.Rule, a.Reason))
			}
		}
	}
	if len(firing) == 0 {
		p("None firing.\n\n")
	} else {
		p("| SLO | Rule | Severity | Why |\n|---|---|---|---|\n%s\n\n", strings.Join(firing, "\n"))
	}
	if len(unsure) > 0 {
		p("%d alert rules cannot be judged (insufficient data):\n\n%s\n\n", len(unsure), strings.Join(unsure, "\n"))
	}

	p("## Top sources of errors\n\n")
	for _, st := range in.States {
		s := st.Status
		e := TopErrors(in.Events, s.Service, s.SLIType, in.Now.Add(-s.Window), in.Now, topN)
		p("### %s\n\n", s.SLO)
		if e.TotalBad == 0 {
			p("No bad events in the window.\n\n")
			continue
		}
		p("%d bad events in the window.\n\n", e.TotalBad)
		writeTable(&b, "Endpoint", e.ByEndpoint, e.TotalBad)
		writeTable(&b, "Status class", e.ByClass, e.TotalBad)
	}

	p("## Forecast\n\n")
	for _, st := range in.States {
		p("- **%s**: %s\n", st.Status.SLO, st.Forecast.Explain())
	}
	p("\n## Data quality\n\n")
	writeQuality(&b, in.Quality)
	p("_Generated by ebg from recorded data. Secrets and personal data are removed from everything printed._\n")

	return redact.ScrubCount(b.String())
}
