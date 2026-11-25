package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/alert"
	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/forecast"
	"github.com/sonujha78/error-budget-guard/internal/gate"
	"github.com/sonujha78/error-budget-guard/internal/slo"
	"github.com/sonujha78/error-budget-guard/internal/sources"
)

var now = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func mkEv(ago time.Duration, label string, good, valid int64) event.Event {
	return event.Event{Timestamp: now.Add(-ago), Service: "svc", SLI: event.SLIAvailability,
		Good: good, Valid: valid, Source: "test", Label: label}
}

func sloOf() slo.SLO {
	return slo.SLO{Name: "svc-availability", Service: "svc", SLI: event.SLIAvailability,
		Objective: 99, Window: slo.Duration(100 * time.Hour)}
}

func sample() []event.Event {
	return []event.Event{
		mkEv(time.Hour, "/pay|5xx", 0, 50),
		mkEv(2*time.Hour, "/cart|5xx", 0, 10),
		mkEv(3*time.Hour, "/search|2xx", 995, 1000),
		mkEv(4*time.Hour, "/pay|2xx", 1000, 1000),
		mkEv(101*time.Hour, "/old|5xx", 0, 999), // outside the window
	}
}

func policy(t *testing.T) *gate.Policy {
	t.Helper()
	p, err := gate.Parse([]byte("block_below_remaining: 0.10\nwarn_below_remaining: 0.25\non_insufficient: warn\n"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func input(t *testing.T, evs []event.Event, alerts []alert.Alert) Input {
	t.Helper()
	s := sloOf()
	st := budget.ComputeWith(s, evs, now, 0)
	states := []gate.SLOState{{Status: st, Alerts: alerts, Forecast: forecast.Compute(s, evs, now, st, 12)}}
	return Input{Now: now, Events: evs, States: states, Decision: gate.Decide(policy(t), states)}
}

func firingAlert() alert.Alert {
	return alert.Alert{SLO: "svc-availability", Rule: "fast-burn", Severity: alert.SeverityPage, Firing: true,
		Threshold: 14.4, Long: time.Hour, Short: 5 * time.Minute, LongBurn: 20, ShortBurn: 25, BudgetConsumed: 0.03}
}

func TestTopErrors(t *testing.T) {
	e := TopErrors(sample(), "svc", event.SLIAvailability, now.Add(-100*time.Hour), now, 2)
	if e.TotalBad != 65 {
		t.Errorf("total bad = %d, want 65 (the event outside the window must not count)", e.TotalBad)
	}
	if len(e.ByEndpoint) != 2 || e.ByEndpoint[0] != (ErrorSource{"/pay", 50}) || e.ByEndpoint[1] != (ErrorSource{"/cart", 10}) {
		t.Errorf("by endpoint (top 2) = %+v", e.ByEndpoint)
	}
	if len(e.ByClass) != 2 || e.ByClass[0] != (ErrorSource{"5xx", 60}) || e.ByClass[1] != (ErrorSource{"2xx", 5}) {
		t.Errorf("by class = %+v", e.ByClass)
	}
}

func TestTopErrorsTiesAreSortedByName(t *testing.T) {
	evs := []event.Event{mkEv(time.Hour, "/b|5xx", 0, 7), mkEv(time.Hour, "/a|5xx", 0, 7)}
	e := TopErrors(evs, "svc", event.SLIAvailability, now.Add(-100*time.Hour), now, 0)
	if len(e.ByEndpoint) != 2 || e.ByEndpoint[0].Key != "/a" {
		t.Errorf("ties must be sorted by name: %+v", e.ByEndpoint)
	}
}

func TestBuildHasEverySectionInOrder(t *testing.T) {
	md, _ := Build(input(t, sample(), []alert.Alert{firingAlert()}))
	last := -1
	for _, h := range []string{"# Error budget report", "## Gate decision", "## SLO status", "## Active alerts",
		"## Top sources of errors", "## Forecast", "## Data quality"} {
		i := strings.Index(md, h)
		if i < 0 || i < last {
			t.Fatalf("section %q missing or out of order:\n%s", h, md)
		}
		last = i
	}
	for _, want := range []string{
		"**BLOCK** (exit 2)",
		"| svc-availability | 99% | 96.845% | 65.0 of 20.6 events (315.5%) | 0.0% | overspent |",
		"| svc-availability | fast-burn | page |",
		"already used up",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report does not contain %q:\n%s", want, md)
		}
	}
}

func TestBuildNamesTheTopEndpointFirst(t *testing.T) {
	md, _ := Build(input(t, sample(), nil))
	sec := md[strings.Index(md, "## Top sources of errors"):strings.Index(md, "## Forecast")]
	if !strings.Contains(sec, "| /pay | 50 | 76.9% |") {
		t.Errorf("top endpoint row missing:\n%s", sec)
	}
	if strings.Index(sec, "| /pay |") > strings.Index(sec, "| /cart |") {
		t.Error("/pay (50 bad) must be listed before /cart (10 bad)")
	}
}

func TestBuildInsufficientDataShowsNoNumbers(t *testing.T) {
	s := sloOf()
	st := budget.Compute(s, nil, now)
	states := []gate.SLOState{{Status: st, Forecast: forecast.Compute(s, nil, now, st, 12)}}
	md, _ := Build(Input{Now: now, States: states, Decision: gate.Decide(policy(t), states)})
	if !strings.Contains(md, "| svc-availability | 99% | n/a | n/a | n/a | insufficient data |") {
		t.Errorf("row must say n/a:\n%s", md)
	}
	if strings.Contains(md, "100.0%") {
		t.Error("no data must never be shown as a full budget")
	}
	if !strings.Contains(md, "cannot forecast") || !strings.Contains(md, "**WARN**") {
		t.Errorf("forecast and decision must be honest:\n%s", md)
	}
}

func TestBuildShowsTheOverrideReason(t *testing.T) {
	in := input(t, sample(), nil)
	d, err := in.Decision.WithOverride("emergency hotfix for sev1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	in.Decision = d
	md, _ := Build(in)
	for _, want := range []string{"OVERRIDDEN by alice", "emergency hotfix for sev1", "**Override:**", "(exit 0)"} {
		if !strings.Contains(md, want) {
			t.Errorf("report does not contain %q:\n%s", want, md)
		}
	}
	if !strings.Contains(md, "% of the error budget remains") {
		t.Error("the original reasons must stay in the report")
	}
}

func TestBuildScrubsSecrets(t *testing.T) {
	in := input(t, sample(), nil)
	in.Title = "report for 203.0.113.7"
	in.Quality = []Quality{{Source: "nginx-log", Stats: sources.Stats{Lines: 3,
		Warnings: []string{"seen from 198.51.100.9 with token=tok_SECRET123"}}}}
	md, n := Build(in)
	for _, f := range []string{"203.0.113.7", "198.51.100.9", "tok_SECRET123"} {
		if strings.Contains(md, f) {
			t.Errorf("report leaks %q", f)
		}
	}
	if n < 3 {
		t.Errorf("redactions = %d, want at least 3", n)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	a, _ := Build(input(t, sample(), []alert.Alert{firingAlert()}))
	b, _ := Build(input(t, sample(), []alert.Alert{firingAlert()}))
	if a != b {
		t.Error("two builds of the same input differ")
	}
}

func TestBuildEscapesPipesInTables(t *testing.T) {
	evs := []event.Event{mkEv(time.Hour, "/a|b|5xx", 0, 5)}
	md, _ := Build(input(t, evs, nil))
	if !strings.Contains(md, `| /a\|b | 5 | 100.0% |`) {
		t.Errorf("a pipe inside an endpoint must be escaped:\n%s", md)
	}
}

func TestDataQualitySection(t *testing.T) {
	md, _ := Build(input(t, sample(), nil))
	if !strings.Contains(md, "No parse statistics were supplied") {
		t.Errorf("must say that missing data was not measured:\n%s", md)
	}
	in := input(t, sample(), nil)
	in.Quality = []Quality{{
		Source:   "probe",
		Stats:    sources.Stats{Lines: 6, Malformed: 1, Requests: 5, Events: 5, Warnings: []string{"w1"}, Errors: []string{"line 2: bad"}},
		Coverage: &Coverage{Kind: "coverage", Samples: 6, SpanSeconds: 100, MissingSeconds: 40, Ratio: 0.6, Gaps: 1},
	}}
	md, _ = Build(in)
	for _, want := range []string{"**probe**: 6 lines read, 1 malformed (16.7%)",
		"missing 40s (40.0% of the observed span), gaps: 1", "warning: w1", "parse error: line 2: bad"} {
		if !strings.Contains(md, want) {
			t.Errorf("report does not contain %q:\n%s", want, md)
		}
	}
	in.Quality[0].Coverage = &Coverage{Kind: "silence", Samples: 6, SpanSeconds: 100, MissingSeconds: 40, Gaps: 1}
	md, _ = Build(in)
	if !strings.Contains(md, "cannot tell a quiet period from lost data") {
		t.Errorf("silence must not be called missing data:\n%s", md)
	}
}

func TestReadQuality(t *testing.T) {
	q := Quality{Source: "probe", Stats: sources.Stats{Lines: 4}, Coverage: &Coverage{Kind: "coverage", Samples: 2}}
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadQuality(strings.NewReader(string(data)))
	if err != nil || got.Source != "probe" || got.Stats.Lines != 4 || got.Coverage == nil || got.Coverage.Samples != 2 {
		t.Errorf("got %+v err=%v", got, err)
	}
	for _, bad := range []string{"", "not json", `{"source":"x","ip":"203.0.113.7"}`, `{"stats":{}}`, string(data) + `{}`} {
		_, err := ReadQuality(strings.NewReader(bad))
		if err == nil {
			t.Errorf("%q must be refused", bad)
		} else if strings.Contains(err.Error(), "203.0.113.7") {
			t.Error("error text echoes input")
		}
	}
}
