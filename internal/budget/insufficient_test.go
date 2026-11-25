package budget

import (
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
)

func TestFalseComfortOnTinyTraffic(t *testing.T) {
	// 3 requests, 0 failures: SLI 100% and "full budget" would be false comfort.
	evs := []event.Event{ev(now.Add(-time.Minute), "svc", event.SLIAvailability, 3, 3)}
	st := Compute(sloOf(99.9, 720*time.Hour), evs, now)
	if !st.HasData || !st.Insufficient || !strings.Contains(st.Reason, "less than one event") {
		t.Errorf("status = %+v", st)
	}
}

func TestEnoughTrafficIsNotInsufficient(t *testing.T) {
	evs := []event.Event{ev(now.Add(-time.Hour), "svc", event.SLIAvailability, 999500, 1000000)}
	st := Compute(sloOf(99.9, 720*time.Hour), evs, now)
	if st.Insufficient {
		t.Errorf("1,000,000 requests are enough: %+v", st)
	}
	near(t, "coverage", st.HistoryCoverage, 1.0/720.0, Tolerance)
}

func TestNoDataIsInsufficient(t *testing.T) {
	st := Compute(sloOf(99.9, 720*time.Hour), nil, now)
	if st.HasData || !st.Insufficient || st.HistoryCoverage != 0 {
		t.Errorf("status = %+v", st)
	}
}

func TestComputeWithMinimumCoverage(t *testing.T) {
	s := sloOf(99.9, 720*time.Hour)
	short := []event.Event{ev(now.Add(-time.Hour), "svc", event.SLIAvailability, 999500, 1000000)}
	if st := ComputeWith(s, short, now, 0.8); !st.Insufficient || !strings.Contains(st.Reason, "history covers") {
		t.Errorf("1h of history for a 30d window must be insufficient at 80%%: %+v", st)
	}
	full := []event.Event{
		ev(now.Add(-720*time.Hour), "svc", event.SLIAvailability, 1, 1),
		ev(now.Add(-time.Hour), "svc", event.SLIAvailability, 999500, 1000000),
	}
	if st := ComputeWith(s, full, now, 0.8); st.Insufficient {
		t.Errorf("a full window of history is enough: %+v", st)
	}
}
