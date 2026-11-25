package alert

import (
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

var t0 = time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.12f, want %.12f (tolerance %g)", name, got, want, tol)
	}
}

func shipped(t *testing.T) *Policy {
	t.Helper()
	p, err := Load("../../configs/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sloOf(objective float64, window time.Duration) slo.SLO {
	return slo.SLO{Name: "t", Service: "svc", SLI: event.SLIAvailability, Objective: objective, Window: slo.Duration(window)}
}

func ev(ts time.Time, good, valid int64) event.Event {
	return event.Event{Timestamp: ts, Service: "svc", SLI: event.SLIAvailability, Good: good, Valid: valid, Source: "test", Label: "/|2xx"}
}

func byName(t *testing.T, as []Alert, name string) Alert {
	t.Helper()
	for _, a := range as {
		if a.Rule == name {
			return a
		}
	}
	t.Fatalf("no alert for rule %q", name)
	return Alert{}
}

// perMinute makes one event per minute from t0. bad(i) of the 1000 requests in minute i failed.
func perMinute(n int, bad func(i int) int64) []event.Event {
	evs := make([]event.Event, 0, n)
	for i := 0; i < n; i++ {
		evs = append(evs, ev(t0.Add(time.Duration(i)*time.Minute), 1000-bad(i), 1000))
	}
	return evs
}

const onePolicyYAML = `
reference_window: 30d
rules:
  - name: fast-burn
    severity: page
    burn: 14.4
    long: 1h
    short: 5m
`

func TestParsePolicyErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"empty file", "", "empty"},
		{"no rules", "reference_window: 30d\nrules: []\n", "no rules"},
		{"unknown field", strings.Replace(onePolicyYAML, "burn: 14.4", "burnn: 14.4", 1), "burnn"},
		{"bad severity", strings.Replace(onePolicyYAML, "severity: page", "severity: pager", 1), "severity must be"},
		{"short not shorter", strings.Replace(onePolicyYAML, "short: 5m", "short: 2h", 1), "shorter"},
		{"zero burn", strings.Replace(onePolicyYAML, "burn: 14.4", "burn: 0", 1), "burn must be"},
		{"long over reference", strings.Replace(onePolicyYAML, "long: 1h", "long: 31d", 1), "must not exceed"},
		{"no reference window", strings.Replace(onePolicyYAML, "reference_window: 30d\n", "", 1), "reference_window"},
		{"duplicate name", onePolicyYAML + "  - name: fast-burn\n    severity: ticket\n    burn: 1\n    long: 3d\n    short: 6h\n", "duplicate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err=%v, want substring %q", err, c.want)
			}
		})
	}
}

// The doc's worked examples: 14.4x/1h = 2%, 6x/6h = 5%, 1x/3d = 10% of a 30 day budget.
func TestShippedPolicyMatchesTheDoc(t *testing.T) {
	p := shipped(t)
	want := []struct {
		name, sev   string
		burn        float64
		long, short time.Duration
		consumed    float64
	}{
		{"fast-burn", SeverityPage, 14.4, time.Hour, 5 * time.Minute, 0.02},
		{"medium-burn", SeverityPage, 6, 6 * time.Hour, 30 * time.Minute, 0.05},
		{"slow-burn", SeverityTicket, 1, 72 * time.Hour, 6 * time.Hour, 0.10},
	}
	if len(p.Rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(p.Rules))
	}
	for i, w := range want {
		r := p.Rules[i]
		if r.Name != w.name || r.Severity != w.sev || r.Burn != w.burn || r.Long.Std() != w.long || r.Short.Std() != w.short {
			t.Errorf("rule %d = %+v", i, r)
		}
		near(t, w.name+" budget used", budget.ConsumedByBurn(r.Burn, r.Long.Std(), p.ReferenceWindow.Std()), w.consumed, budget.Tolerance)
	}
}

func TestScaleDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		f    float64
		want time.Duration
	}{
		{time.Hour, 1.0 / 60, time.Minute},
		{5 * time.Minute, 1.0 / 60, 5 * time.Second},
		{72 * time.Hour, 1.0 / 60, 72 * time.Minute},
		{time.Hour, float64(30*time.Minute) / float64(720*time.Hour), 2500 * time.Millisecond},
		{time.Hour, 1, time.Hour},
		{time.Second, 1e-9, time.Millisecond},
	}
	for _, c := range cases {
		if got := ScaleDuration(c.in, c.f); got != c.want {
			t.Errorf("ScaleDuration(%v, %g) = %v, want %v", c.in, c.f, got, c.want)
		}
	}
}

func TestDemoSLOScalesToOneSixtieth(t *testing.T) {
	f, err := slo.Load("../../configs/demo-slos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p := shipped(t)
	s := f.SLOs[0]
	scale := p.Scale(s.Window.Std())
	near(t, "scale", scale, 1.0/60, 1e-12)
	wantWin := [][2]time.Duration{{time.Minute, 5 * time.Second}, {6 * time.Minute, 30 * time.Second}, {72 * time.Minute, 6 * time.Minute}}
	wantUsed := []float64{0.02, 0.05, 0.10}
	for i, r := range p.Rules {
		l, sh := ScaleDuration(r.Long.Std(), scale), ScaleDuration(r.Short.Std(), scale)
		if l != wantWin[i][0] || sh != wantWin[i][1] {
			t.Errorf("%s: scaled windows = %v/%v, want %v/%v", r.Name, l, sh, wantWin[i][0], wantWin[i][1])
		}
		near(t, r.Name+" budget used", budget.ConsumedByBurn(r.Burn, l, s.Window.Std()), wantUsed[i], budget.Tolerance)
	}
}

func TestFastBurnWorkedExample(t *testing.T) {
	// 99.9% SLO, 1.44% errors for one hour => burn 14.4 => 2% of the 30 day budget.
	now := t0.Add(6 * time.Hour)
	var evs []event.Event
	for i := 0; i < 60; i++ {
		evs = append(evs, ev(now.Add(-time.Duration(i)*time.Minute), 2464, 2500))
	}
	a := byName(t, Evaluate(shipped(t), sloOf(99.9, 720*time.Hour), evs, now), "fast-burn")
	if !a.Firing {
		t.Fatalf("fast-burn must fire at exactly 14.4x: %+v", a)
	}
	near(t, "long burn", a.LongBurn, 14.4, budget.Tolerance)
	near(t, "short burn", a.ShortBurn, 14.4, budget.Tolerance)
	near(t, "budget consumed", a.BudgetConsumed, 0.02, budget.Tolerance)
	msg := a.Explain()
	for _, want := range []string{"14.4x over 1h0m0s", "5m0s", "2.00% of the error budget"} {
		if !strings.Contains(msg, want) {
			t.Errorf("explanation %q does not contain %q", msg, want)
		}
	}
}

func TestScaledWindowsKeepTheMaths(t *testing.T) {
	// 12h SLO window = 1/60 scale: fast-burn windows are 1m and 5s. Same burn, same 2%.
	now := t0.Add(time.Hour)
	var evs []event.Event
	for i := 0; i < 60; i++ {
		evs = append(evs, ev(now.Add(-time.Duration(i)*time.Second), 2464, 2500))
	}
	a := byName(t, Evaluate(shipped(t), sloOf(99.9, 12*time.Hour), evs, now), "fast-burn")
	if !a.Firing || a.Long != time.Minute || a.Short != 5*time.Second {
		t.Fatalf("alert = %+v", a)
	}
	near(t, "budget consumed", a.BudgetConsumed, 0.02, budget.Tolerance)
}

func TestThresholdIsInclusive(t *testing.T) {
	p, err := Parse([]byte(onePolicyYAML))
	if err != nil {
		t.Fatal(err)
	}
	s := sloOf(99.9, 720*time.Hour)
	now := t0.Add(2 * time.Hour)
	mk := func(good int64) []event.Event {
		var evs []event.Event
		for i := 0; i < 60; i++ {
			evs = append(evs, ev(now.Add(-time.Duration(i)*time.Minute), good, 2500))
		}
		return evs
	}
	if a := Evaluate(p, s, mk(2464), now)[0]; !a.Firing { // 36 bad of 2500 = burn 14.4
		t.Errorf("exactly at the threshold must fire: %+v", a)
	}
	if a := Evaluate(p, s, mk(2465), now)[0]; a.Firing { // 35 bad of 2500 = burn 14.0
		t.Errorf("just below the threshold must not fire: %+v", a)
	}
}

func TestShortWindowPreventsStaleAlert(t *testing.T) {
	// Heavy errors for 55 minutes, then 5 clean minutes: the long window is still hot,
	// the short window says it is over, so there is no alert.
	now := t0.Add(6 * time.Hour)
	var evs []event.Event
	for i := 0; i < 60; i++ {
		good := int64(800)
		if i < 5 {
			good = 1000
		}
		evs = append(evs, ev(now.Add(-time.Duration(i)*time.Minute), good, 1000))
	}
	a := byName(t, Evaluate(shipped(t), sloOf(99.9, 720*time.Hour), evs, now), "fast-burn")
	if a.Firing || a.LongBurn < 100 || a.ShortBurn != 0 {
		t.Errorf("alert = %+v, want long burn high, short burn 0, not firing", a)
	}
}

func TestShortSpikeAloneDoesNotFire(t *testing.T) {
	// 5 minutes at 10% errors after a perfect hour: short window is hot, long window is not.
	now := t0.Add(6 * time.Hour)
	var evs []event.Event
	for i := 0; i < 60; i++ {
		good := int64(1000)
		if i < 5 {
			good = 900
		}
		evs = append(evs, ev(now.Add(-time.Duration(i)*time.Minute), good, 1000))
	}
	a := byName(t, Evaluate(shipped(t), sloOf(99.9, 720*time.Hour), evs, now), "fast-burn")
	if a.Firing {
		t.Errorf("a short spike alone must not page: %+v", a)
	}
	near(t, "long burn", a.LongBurn, 500.0/60000.0/0.001, 1e-9)
	near(t, "short burn", a.ShortBurn, 100, 1e-9)
}

func TestNoDataDoesNotFire(t *testing.T) {
	as := Evaluate(shipped(t), sloOf(99.9, 720*time.Hour), nil, t0)
	if len(as) != 3 {
		t.Fatalf("got %d alerts, want one per rule (3)", len(as))
	}
	for _, a := range as {
		if a.Firing || a.LongKnown || a.ShortKnown || a.BudgetConsumed != 0 {
			t.Errorf("no data must not fire: %+v", a)
		}
		if !strings.Contains(a.Explain(), "no data") {
			t.Errorf("explanation should say there is no data: %q", a.Explain())
		}
	}
}

type wantSpan struct {
	start, end int
	open       bool
	peak       float64
}

func pattern(s string) []point {
	pts := make([]point, len(s))
	for i, c := range s {
		pts[i] = point{at: t0.Add(time.Duration(i) * time.Second), firing: c == '1', burn: float64(i)}
	}
	return pts
}

func TestBuildSpans(t *testing.T) {
	hold := 10 * time.Second
	cases := []struct {
		name string
		pat  string
		want []wantSpan
	}{
		{"never fires", "0000", nil},
		{"closed episode", "0110", []wantSpan{{1, 2, false, 2}}},
		{"short pause is one episode", "111111" + "00000" + "11111", []wantSpan{{0, 15, true, 15}}},
		{"long pause is two episodes", "111111" + strings.Repeat("0", 25) + "11111", []wantSpan{{0, 5, false, 5}, {31, 35, true, 35}}},
		{"pause of exactly the hold time merges", "1" + strings.Repeat("0", 10) + "1", []wantSpan{{0, 11, true, 11}}},
		{"one second longer splits", "1" + strings.Repeat("0", 11) + "1", []wantSpan{{0, 0, false, 0}, {12, 12, true, 12}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildSpans(pattern(c.pat), hold)
			if len(got) != len(c.want) {
				t.Fatalf("got %d spans %+v, want %d", len(got), got, len(c.want))
			}
			for i, w := range c.want {
				g := got[i]
				if !g.start.Equal(t0.Add(time.Duration(w.start)*time.Second)) || !g.end.Equal(t0.Add(time.Duration(w.end)*time.Second)) ||
					g.open != w.open || g.peak != w.peak {
					t.Errorf("span %d = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

func outageEvents() []event.Event {
	return perMinute(220, func(i int) int64 {
		if i >= 150 && i <= 169 { // 20 minutes of total outage
			return 1000
		}
		return 0
	})
}

func TestOutageGivesOneFastBurnEpisodeWithoutFlapping(t *testing.T) {
	eps, err := Episodes(shipped(t), sloOf(99.9, 720*time.Hour), outageEvents(), t0.Add(120*time.Minute), t0.Add(219*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var fast []Episode
	for _, e := range eps {
		if e.Rule == "fast-burn" {
			fast = append(fast, e)
		}
	}
	if len(fast) != 1 {
		t.Fatalf("fast-burn episodes = %d, want exactly 1 (no flapping): %+v", len(fast), eps)
	}
	e := fast[0]
	outageStart, outageEnd := t0.Add(150*time.Minute), t0.Add(169*time.Minute)
	if d := e.Start.Sub(outageStart); d < 0 || d > time.Minute {
		t.Errorf("detection delay = %v, want within 1m", d)
	}
	if e.End.Before(outageEnd) || e.End.After(outageEnd.Add(6*time.Minute)) {
		t.Errorf("episode ended at %v, want within 6m after the outage ended at %v", e.End, outageEnd)
	}
	if e.Open || e.Severity != SeverityPage || e.PeakBurn < 100 || e.SLO != "t" {
		t.Errorf("episode = %+v", e)
	}
}

func TestHealthyTrafficHasNoEpisodes(t *testing.T) {
	var evs []event.Event
	for i := 0; i < 180; i++ {
		evs = append(evs, ev(t0.Add(time.Duration(i)*time.Minute), 1999, 2000)) // 0.05% errors = burn 0.5
	}
	eps, err := Episodes(shipped(t), sloOf(99.9, 720*time.Hour), evs, t0.Add(10*time.Minute), t0.Add(179*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 0 {
		t.Errorf("healthy traffic must not alert: %+v", eps)
	}
}

func TestEpisodesDoNotDependOnEventOrder(t *testing.T) {
	a := outageEvents()
	b := append([]event.Event(nil), a...)
	rand.New(rand.NewSource(3)).Shuffle(len(b), func(i, j int) { b[i], b[j] = b[j], b[i] })
	p, s := shipped(t), sloOf(99.9, 720*time.Hour)
	from, to := t0.Add(120*time.Minute), t0.Add(219*time.Minute)
	ea, err1 := Episodes(p, s, a, from, to, time.Minute)
	eb, err2 := Episodes(p, s, b, from, to, time.Minute)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if !reflect.DeepEqual(ea, eb) {
		t.Error("episodes depend on the order of the input events")
	}
}

func TestEpisodesRejectBadInput(t *testing.T) {
	p, s := shipped(t), sloOf(99.9, 720*time.Hour)
	if _, err := Episodes(p, s, nil, t0, t0.Add(time.Hour), 0); err == nil {
		t.Error("zero step must be an error")
	}
	if _, err := Episodes(p, s, nil, t0.Add(time.Hour), t0, time.Minute); err == nil {
		t.Error("to before from must be an error")
	}
	if _, err := Episodes(p, s, nil, t0, t0.Add(100*24*time.Hour), time.Millisecond); err == nil {
		t.Error("an absurd number of steps must be an error")
	}
}
