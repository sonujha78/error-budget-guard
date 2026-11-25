package forecast

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

var now = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func sloOf() slo.SLO {
	return slo.SLO{Name: "t", Service: "svc", SLI: event.SLIAvailability, Objective: 99.9, Window: slo.Duration(720 * time.Hour)}
}

// hourly makes n events, one per hour, the newest at now. good(i) is the good count i hours ago.
func hourly(n int, valid int64, good func(i int) int64) []event.Event {
	var evs []event.Event
	for i := 0; i < n; i++ {
		evs = append(evs, event.Event{
			Timestamp: now.Add(-time.Duration(i) * time.Hour), Service: "svc", SLI: event.SLIAvailability,
			Good: good(i), Valid: valid, Source: "test", Label: "/|2xx",
		})
	}
	return evs
}

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.9f, want %.9f (tolerance %g)", name, got, want, tol)
	}
}

func TestTimeToExhaustionWorkedExamples(t *testing.T) {
	const month = 720 * time.Hour
	cases := []struct {
		name      string
		remaining float64
		burn      float64
		want      time.Duration
	}{
		{"half left at burn 1 lasts half a month", 0.5, 1, 360 * time.Hour},
		{"half left at burn 2", 0.5, 2, 180 * time.Hour},
		{"full budget at 14.4x lasts 50 hours", 1, 14.4, 50 * time.Hour},
		{"nothing left", 0, 5, 0},
	}
	for _, c := range cases {
		got, ok := TimeToExhaustion(c.remaining, c.burn, month)
		if !ok || (got-c.want > time.Millisecond) || (c.want-got > time.Millisecond) {
			t.Errorf("%s: got %v ok=%v, want %v", c.name, got, ok, c.want)
		}
	}
	if _, ok := TimeToExhaustion(0.5, 0, month); ok {
		t.Error("burn 0 never exhausts the budget")
	}
	if got, ok := TimeToExhaustion(1, 1e-12, month); !ok || got != MaxHorizon {
		t.Errorf("a tiny burn must be capped at the horizon, got %v ok=%v", got, ok)
	}
}

func TestSteadyBurnForecast(t *testing.T) {
	// 1 bad in 2000 requests every hour for 30 days: burn 0.5, half of the budget used.
	// The remaining 720 bad events are used at 1 per hour, so the true time left is 720h.
	s := sloOf()
	evs := hourly(720, 2000, func(i int) int64 { return 1999 })
	st := budget.ComputeWith(s, evs, now, 0.8)
	if st.Insufficient {
		t.Fatalf("status must be sufficient: %+v", st)
	}
	f := Compute(s, evs, now, st, 12)
	if !f.Known || f.Exhausted || f.NoExhaustion {
		t.Fatalf("forecast = %+v", f)
	}
	near(t, "burn", f.Burn, 0.5, 1e-6)
	near(t, "remaining", f.RemainingFraction, 0.5, 1e-6)
	near(t, "hours left", f.TimeToExhaustion.Hours(), 720, 0.01)
	if f.Window != 60*time.Hour {
		t.Errorf("forecast window = %v, want 60h (a twelfth of 30 days)", f.Window)
	}
	if !strings.Contains(f.Explain(), "lasts") {
		t.Errorf("explanation = %q", f.Explain())
	}
}

func TestZeroBurnNeverExhausts(t *testing.T) {
	s := sloOf()
	evs := hourly(720, 2000, func(i int) int64 { return 2000 })
	f := Compute(s, evs, now, budget.ComputeWith(s, evs, now, 0.8), 12)
	if !f.Known || !f.NoExhaustion || f.Exhausted {
		t.Errorf("forecast = %+v", f)
	}
}

func TestOverspentBudgetIsExhausted(t *testing.T) {
	s := sloOf()
	evs := hourly(720, 1000, func(i int) int64 { return 998 }) // 0.2% errors: burn 2
	f := Compute(s, evs, now, budget.ComputeWith(s, evs, now, 0.8), 12)
	if !f.Known || !f.Exhausted || f.RemainingFraction != 0 {
		t.Errorf("forecast = %+v", f)
	}
	if !strings.Contains(f.Explain(), "already") {
		t.Errorf("explanation = %q", f.Explain())
	}
}

func TestInsufficientBudgetMeansNoForecast(t *testing.T) {
	s := sloOf()
	f := Compute(s, nil, now, budget.Compute(s, nil, now), 12)
	if f.Known || !strings.Contains(f.Reason, "insufficient") || f.Explain() != f.Reason {
		t.Errorf("forecast = %+v", f)
	}
}

func TestShortHistoryMeansNoForecast(t *testing.T) {
	s := sloOf()
	evs := hourly(10, 1000, func(i int) int64 {
		if i == 0 {
			return 999
		}
		return 1000
	})
	st := budget.ComputeWith(s, evs, now, 0) // coverage of the SLO window is not asked here
	if st.Insufficient {
		t.Fatalf("status = %+v", st)
	}
	f := Compute(s, evs, now, st, 12)
	if f.Known || !strings.Contains(f.Reason, "history") {
		t.Errorf("9h of history cannot forecast with a 60h window: %+v", f)
	}
}

func TestDivisor(t *testing.T) {
	s := sloOf()
	evs := hourly(720, 2000, func(i int) int64 { return 1999 })
	st := budget.ComputeWith(s, evs, now, 0.8)
	if w := Compute(s, evs, now, st, 0).Window; w != 60*time.Hour {
		t.Errorf("divisor 0 must mean the default 12, window = %v", w)
	}
	if w := Compute(s, evs, now, st, 24).Window; w != 30*time.Hour {
		t.Errorf("divisor 24: window = %v, want 30h", w)
	}
}
