package budget

import (
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

var now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.12f, want %.12f (tolerance %g)", name, got, want, tol)
	}
}

func ev(ts time.Time, service, sli string, good, valid int64) event.Event {
	return event.Event{Timestamp: ts, Service: service, SLI: sli, Good: good, Valid: valid, Source: "test", Label: "x"}
}

func sloOf(objective float64, window time.Duration) slo.SLO {
	return slo.SLO{
		Name:      "t",
		Service:   "svc",
		SLI:       event.SLIAvailability,
		Objective: objective,
		Window:    slo.Duration(window),
	}
}

// Worked examples from the Google SRE workbook: burn x duration / window.
func TestConsumedByBurnWorkedExamples(t *testing.T) {
	const month = 720 * time.Hour
	cases := []struct {
		name string
		burn float64
		dur  time.Duration
		want float64
	}{
		{"14.4x for 1h uses 2%", 14.4, time.Hour, 0.02},
		{"6x for 6h uses 5%", 6, 6 * time.Hour, 0.05},
		{"1x for 3d uses 10%", 1, 72 * time.Hour, 0.10},
		{"1x for the whole window uses 100%", 1, month, 1.0},
		{"zero burn uses nothing", 0, time.Hour, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			near(t, "consumed", ConsumedByBurn(c.burn, c.dur, month), c.want, Tolerance)
		})
	}
	if got := ConsumedByBurn(10, time.Hour, 0); got != 0 {
		t.Errorf("zero window must give 0, got %v", got)
	}
}

// End to end: 99.9% SLO, 1.44% errors for 1 hour => burn 14.4 => 2% of a 30d budget.
func TestFastBurnWorkedExample(t *testing.T) {
	var evs []event.Event
	for i := 0; i < 60; i++ {
		ts := now.Add(-time.Duration(i) * time.Minute)
		evs = append(evs, ev(ts, "svc", event.SLIAvailability, 2464, 2500)) // 36 bad of 2500 = 1.44%
	}
	s := sloOf(99.9, 720*time.Hour)
	burn, ok := BurnOver(s, evs, now, time.Hour)
	if !ok {
		t.Fatal("expected burn rate to be computable")
	}
	near(t, "burn rate", burn, 14.4, Tolerance)
	near(t, "budget consumed", ConsumedByBurn(burn, time.Hour, s.Window.Std()), 0.02, Tolerance)
}

func TestComputeWorkedExample(t *testing.T) {
	// 99.9% objective, 1,000,000 valid, 500 bad => SLI 99.95%, half the budget used.
	evs := []event.Event{ev(now.Add(-time.Hour), "svc", event.SLIAvailability, 999500, 1000000)}
	st := Compute(sloOf(99.9, 720*time.Hour), evs, now)
	if !st.HasData {
		t.Fatal("HasData should be true")
	}
	near(t, "SLI", st.SLI, 0.9995, Tolerance)
	near(t, "budget total", st.BudgetTotal, 1000, 1e-6)
	near(t, "budget used", st.BudgetUsed, 500, 1e-6)
	near(t, "budget remaining", st.BudgetRemaining, 500, 1e-6)
	near(t, "used fraction", st.UsedFraction, 0.5, Tolerance)
	if st.Overspent {
		t.Error("should not be overspent")
	}
}

func TestComputeOverspentIsClampedNotNegative(t *testing.T) {
	evs := []event.Event{ev(now.Add(-time.Hour), "svc", event.SLIAvailability, 998000, 1000000)}
	st := Compute(sloOf(99.9, 720*time.Hour), evs, now)
	near(t, "budget used", st.BudgetUsed, 2000, 1e-6)
	if st.BudgetRemaining != 0 {
		t.Errorf("remaining = %v, want 0 (never negative)", st.BudgetRemaining)
	}
	near(t, "used fraction", st.UsedFraction, 2.0, Tolerance)
	if !st.Overspent {
		t.Error("Overspent should be true")
	}
}

func TestComputeNoData(t *testing.T) {
	st := Compute(sloOf(99.9, 720*time.Hour), nil, now)
	if st.HasData {
		t.Error("HasData must be false with no events")
	}
	if st.BudgetTotal != 0 || st.BudgetRemaining != 0 || st.Overspent {
		t.Errorf("no data must not produce budget numbers: %+v", st)
	}
}

func TestWindowBoundaries(t *testing.T) {
	w := 720 * time.Hour
	evs := []event.Event{
		ev(now.Add(-w), "svc", event.SLIAvailability, 1, 1),                // exactly at start: excluded
		ev(now.Add(-w+time.Second), "svc", event.SLIAvailability, 10, 10),  // just inside
		ev(now, "svc", event.SLIAvailability, 100, 100),                    // exactly at end: included
		ev(now.Add(time.Second), "svc", event.SLIAvailability, 1000, 1000), // future: excluded
	}
	c := Sum(evs, "svc", event.SLIAvailability, now.Add(-w), now)
	if c.Valid != 110 || c.Good != 110 {
		t.Fatalf("counts = %+v, want valid=110 good=110", c)
	}
}

func TestFiltersByServiceAndSLI(t *testing.T) {
	evs := []event.Event{
		ev(now.Add(-time.Minute), "svc", event.SLIAvailability, 9, 10),
		ev(now.Add(-time.Minute), "other", event.SLIAvailability, 0, 100),
		ev(now.Add(-time.Minute), "svc", event.SLILatency, 0, 1000),
	}
	st := Compute(sloOf(99, time.Hour), evs, now)
	if st.Counts.Valid != 10 || st.Counts.Good != 9 {
		t.Fatalf("counts = %+v, want good=9 valid=10", st.Counts)
	}
}

func TestShortAndLongWindowsDiffer(t *testing.T) {
	// Last 5 minutes: 10% bad. Before that: perfect.
	var evs []event.Event
	for i := 0; i < 60; i++ {
		ts := now.Add(-time.Duration(i) * time.Minute)
		good := int64(100)
		if i < 5 {
			good = 90
		}
		evs = append(evs, ev(ts, "svc", event.SLIAvailability, good, 100))
	}
	s := sloOf(99.9, 720*time.Hour)
	short, ok1 := BurnOver(s, evs, now, 5*time.Minute)
	long, ok2 := BurnOver(s, evs, now, time.Hour)
	if !ok1 || !ok2 {
		t.Fatal("burn rates should be computable")
	}
	near(t, "5m burn", short, 100, Tolerance)              // 10% / 0.1%
	near(t, "1h burn", long, 50.0/6000.0/0.001, Tolerance) // 0.833% / 0.1%
}

func TestGuards(t *testing.T) {
	if _, ok := SLI(Counts{}); ok {
		t.Error("SLI with zero valid must not be ok")
	}
	if _, ok := BurnRate(Counts{}, 0.001); ok {
		t.Error("BurnRate with zero valid must not be ok")
	}
	if _, ok := BurnRate(Counts{Good: 1, Valid: 1}, 0); ok {
		t.Error("BurnRate with zero budget ratio must not be ok")
	}
	b, ok := BurnRate(Counts{Good: 10, Valid: 10}, 0.001)
	if !ok || b != 0 {
		t.Errorf("perfect traffic: burn=%v ok=%v, want 0 true", b, ok)
	}
}

func TestComputeIsOrderIndependent(t *testing.T) {
	var a []event.Event
	for i := 0; i < 30; i++ {
		a = append(a, ev(now.Add(-time.Duration(i)*time.Minute), "svc", event.SLIAvailability, int64(90+i%10), 100))
	}
	b := append([]event.Event(nil), a...)
	rand.New(rand.NewSource(7)).Shuffle(len(b), func(i, j int) { b[i], b[j] = b[j], b[i] })
	s := sloOf(99.5, 24*time.Hour)
	if !reflect.DeepEqual(Compute(s, a, now), Compute(s, b, now)) {
		t.Fatal("Compute depends on input order")
	}
}
