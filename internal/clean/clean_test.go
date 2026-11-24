package clean

import (
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
)

var t0 = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func times(secs ...int) []time.Time {
	out := make([]time.Time, len(secs))
	for i, s := range secs {
		out[i] = at(s)
	}
	return out
}

func ev(source string, ts time.Time) event.Event {
	return event.Event{Timestamp: ts, Service: "checkout", SLI: event.SLIAvailability, Good: 1, Valid: 1, Source: source, Label: "/|2xx"}
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %.12f, want %.12f", name, got, want)
	}
}

func TestParseSkew(t *testing.T) {
	sk, err := ParseSkew("nginx-log=+3s, probe=-2s")
	if err != nil {
		t.Fatal(err)
	}
	if len(sk) != 2 || sk["nginx-log"] != 3*time.Second || sk["probe"] != -2*time.Second {
		t.Errorf("skew = %v", sk)
	}
	empty, err := ParseSkew("  ")
	if err != nil || len(empty) != 0 {
		t.Errorf("empty input: skew=%v err=%v", empty, err)
	}
	for _, bad := range []string{"nginx-log", "=3s", "probe=", "probe=3", "probe=abc", "probe=+10m", "probe=-10m", "probe=1s,probe=2s"} {
		if _, err := ParseSkew(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	if _, err := ParseSkew("probe=+10m"); err == nil || !strings.Contains(err.Error(), "not clock skew") {
		t.Errorf("a 10 minute offset must be refused as not clock skew, got %v", err)
	}
}

func TestApplySkew(t *testing.T) {
	in := []event.Event{ev("nginx-log", at(3)), ev("probe", at(3)), ev("other", at(3))}
	out, unused := ApplySkew(in, Skew{"nginx-log": 3 * time.Second, "probe": -2 * time.Second, "ghost": time.Second})
	if !out[0].Timestamp.Equal(at(0)) {
		t.Errorf("clock 3s ahead: got %v, want %v", out[0].Timestamp, at(0))
	}
	if !out[1].Timestamp.Equal(at(5)) {
		t.Errorf("clock 2s behind: got %v, want %v", out[1].Timestamp, at(5))
	}
	if !out[2].Timestamp.Equal(at(3)) {
		t.Errorf("source without skew must not move: got %v", out[2].Timestamp)
	}
	if !in[0].Timestamp.Equal(at(3)) {
		t.Error("input slice was modified")
	}
	if !reflect.DeepEqual(unused, []string{"ghost"}) {
		t.Errorf("unused = %v, want [ghost]", unused)
	}
}

func TestBound(t *testing.T) {
	now := at(100)
	in := []event.Event{ev("a", at(50)), ev("a", at(100)), ev("a", at(102)), ev("a", at(105)), ev("a", at(106))}
	out, rep := Bound(in, now, 5*time.Second)
	if len(out) != 4 || rep.Clamped != 2 || rep.Dropped != 1 {
		t.Fatalf("len=%d report=%+v, want 4 events, 2 clamped, 1 dropped", len(out), rep)
	}
	if !out[2].Timestamp.Equal(now) || !out[3].Timestamp.Equal(now) {
		t.Errorf("events inside the tolerance must be clamped to now: %v %v", out[2].Timestamp, out[3].Timestamp)
	}
	if !in[2].Timestamp.Equal(at(102)) {
		t.Error("input slice was modified")
	}
}

func TestCoverageWorkedExample(t *testing.T) {
	// A probe every 10s for 5 minutes. The samples at 130..180s are missing (6 of them).
	var secs []int
	for s := 10; s <= 300; s += 10 {
		if s >= 130 && s <= 180 {
			continue
		}
		secs = append(secs, s)
	}
	c, err := MeasureCoverage(times(secs...), at(0), at(300), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if c.Samples != 24 || c.Missing != 60*time.Second || c.Span != 300*time.Second {
		t.Errorf("samples=%d missing=%v span=%v, want 24, 60s, 5m", c.Samples, c.Missing, c.Span)
	}
	near(t, "ratio", c.Ratio, 0.8)
	if len(c.Gaps) != 1 || !c.Gaps[0].Start.Equal(at(120)) || !c.Gaps[0].End.Equal(at(190)) {
		t.Errorf("gaps = %+v, want one gap from 120s to 190s", c.Gaps)
	}
}

func TestCoverageLeadingAndTrailingGaps(t *testing.T) {
	var lead, trail []int
	for s := 60; s <= 300; s += 10 {
		lead = append(lead, s)
	}
	for s := 10; s <= 240; s += 10 {
		trail = append(trail, s)
	}
	c, _ := MeasureCoverage(times(lead...), at(0), at(300), 10*time.Second)
	if c.Missing != 50*time.Second || len(c.Gaps) != 1 || !c.Gaps[0].Start.Equal(at(0)) || !c.Gaps[0].End.Equal(at(60)) {
		t.Errorf("leading gap: %+v", c)
	}
	near(t, "leading ratio", c.Ratio, 1-50.0/300.0)
	c, _ = MeasureCoverage(times(trail...), at(0), at(300), 10*time.Second)
	if c.Missing != 50*time.Second || len(c.Gaps) != 1 || !c.Gaps[0].Start.Equal(at(240)) || !c.Gaps[0].End.Equal(at(300)) {
		t.Errorf("trailing gap: %+v", c)
	}
}

func TestCoverageNoSamples(t *testing.T) {
	c, err := MeasureCoverage(nil, at(0), at(300), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if c.Samples != 0 || c.Missing != 300*time.Second || c.Ratio != 0 || len(c.Gaps) != 1 {
		t.Errorf("no samples must mean everything is missing: %+v", c)
	}
}

func TestCoverageJitterIsNotAGap(t *testing.T) {
	c, _ := MeasureCoverage(times(10, 21, 29, 41, 50, 62, 70, 80, 91, 100), at(0), at(100), 10*time.Second)
	if len(c.Gaps) != 0 || c.Missing != 0 || c.Ratio != 1 {
		t.Errorf("jittered samples must show no gap: %+v", c)
	}
}

func TestCoverageGapThreshold(t *testing.T) {
	// interval 10s, limit 15s. A step of exactly 15s is fine, 16s is a gap of 16-10 = 6s.
	c, _ := MeasureCoverage(times(15, 30), at(0), at(30), 10*time.Second)
	if len(c.Gaps) != 0 {
		t.Errorf("15s steps are not gaps: %+v", c)
	}
	c, _ = MeasureCoverage(times(16, 32), at(0), at(32), 10*time.Second)
	if len(c.Gaps) != 2 || c.Missing != 12*time.Second {
		t.Errorf("16s steps are gaps of 6s each: %+v", c)
	}
}

func TestCoverageIgnoresOrderDuplicatesAndOutsiders(t *testing.T) {
	messy := times(30, 10, 20, 10, -5, 400, 300, 0)
	clean := times(10, 20, 30, 300)
	a, err := MeasureCoverage(messy, at(0), at(300), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := MeasureCoverage(clean, at(0), at(300), 10*time.Second)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("messy input must give the same result as clean input:\n%+v\n%+v", a, b)
	}
	if a.Samples != 4 {
		t.Errorf("samples = %d, want 4 (start is exclusive, end inclusive)", a.Samples)
	}
}

func TestCoverageErrors(t *testing.T) {
	if _, err := MeasureCoverage(nil, at(0), at(10), 0); err == nil {
		t.Error("zero interval must be an error")
	}
	if _, err := MeasureCoverage(nil, at(10), at(10), time.Second); err == nil {
		t.Error("empty window must be an error")
	}
}

func TestCoverageInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for n := 0; n < 300; n++ {
		var ts []time.Time
		for i := rng.Intn(40); i > 0; i-- {
			ts = append(ts, at(rng.Intn(400)-50))
		}
		c, err := MeasureCoverage(ts, at(0), at(300), 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if c.Ratio < 0 || c.Ratio > 1 || c.Missing < 0 || c.Missing > c.Span {
			t.Fatalf("invariant broken: %+v", c)
		}
	}
}
