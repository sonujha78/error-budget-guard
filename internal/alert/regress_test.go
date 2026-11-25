package alert

import (
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
)

// Regression tests for the two bugs seen at the end of Step 6. They use only the Step 6 API.

// sparseOneFailure: 60 requests in an hour (one per minute), the newest one failed.
// Burn is 16.7x over 1h and 200x over 5m, but it is one failed request.
func sparseOneFailure(now time.Time) []event.Event {
	var evs []event.Event
	for i := 0; i < 60; i++ {
		good := int64(1)
		if i == 0 {
			good = 0
		}
		evs = append(evs, ev(now.Add(-time.Duration(i)*time.Minute), good, 1))
	}
	return evs
}

func TestRegressionOneFailureInSparseTrafficDoesNotPage(t *testing.T) {
	now := t0.Add(2 * time.Hour)
	a := byName(t, Evaluate(shipped(t), sloOf(99.9, 720*time.Hour), sparseOneFailure(now), now), "fast-burn")
	if a.Firing {
		t.Errorf("one failure in 60 requests must not page: %s", a.Explain())
	}
	if !strings.Contains(a.Explain(), "insufficient") {
		t.Errorf("explanation should say insufficient data: %q", a.Explain())
	}
}

func TestRegressionShortHistoryDoesNotFireSlowRules(t *testing.T) {
	// The real data of Step 6: 110 seconds of history, errors in the last 30 seconds.
	now := t0.Add(110 * time.Second)
	var evs []event.Event
	for i := 0; i <= 110; i++ {
		good := int64(10)
		if i >= 80 {
			good = 0
		}
		evs = append(evs, ev(t0.Add(time.Duration(i)*time.Second), good, 10))
	}
	as := Evaluate(shipped(t), sloOf(99.9, 12*time.Hour), evs, now)
	if f := byName(t, as, "fast-burn"); !f.Firing {
		t.Errorf("fast-burn has enough data and must still fire: %s", f.Explain())
	}
	for _, name := range []string{"medium-burn", "slow-burn"} {
		a := byName(t, as, name)
		if a.Firing || !strings.Contains(a.Explain(), "history") {
			t.Errorf("%s must not fire on 110s of history: firing=%v %s", name, a.Firing, a.Explain())
		}
	}
}
