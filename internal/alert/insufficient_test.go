package alert

import (
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
)

func TestPolicyDefaultsForNewFields(t *testing.T) {
	p, err := Parse([]byte(onePolicyYAML))
	if err != nil {
		t.Fatal(err)
	}
	if p.MinCoverage != 0.8 || p.MinBadAtThreshold != 5 {
		t.Errorf("defaults = %v and %v, want 0.8 and 5", p.MinCoverage, p.MinBadAtThreshold)
	}
}

func TestPolicyValidationOfNewFields(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"coverage above 1", strings.Replace(onePolicyYAML, "reference_window: 30d\n", "reference_window: 30d\nmin_coverage: 1.5\n", 1), "min_coverage must be"},
		{"coverage negative", strings.Replace(onePolicyYAML, "reference_window: 30d\n", "reference_window: 30d\nmin_coverage: -0.1\n", 1), "min_coverage must be"},
		{"negative min bad", strings.Replace(onePolicyYAML, "reference_window: 30d\n", "reference_window: 30d\nmin_bad_at_threshold: -1\n", 1), "min_bad_at_threshold"},
		{"negative min valid", strings.Replace(onePolicyYAML, "short: 5m", "short: 5m\n    min_valid: -3", 1), "min_valid"},
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

func TestRequiredValid(t *testing.T) {
	p := shipped(t)
	s := sloOf(99.9, 720*time.Hour)
	ratio := s.ErrorBudgetRatio()
	want := []int64{348, 834, 5000} // 5 / (0.001 x burn), rounded up
	for i, r := range p.Rules {
		if got := p.RequiredValid(r, ratio); got != want[i] {
			t.Errorf("%s: required valid = %d, want %d", r.Name, got, want[i])
		}
	}
	if got := p.RequiredValid(Rule{Burn: 14.4, MinValid: 77}, ratio); got != 77 {
		t.Errorf("explicit min_valid must win, got %d", got)
	}
}

func TestInsufficientAlertFields(t *testing.T) {
	now := t0.Add(2 * time.Hour)
	a := byName(t, Evaluate(shipped(t), sloOf(99.9, 720*time.Hour), sparseOneFailure(now), now), "fast-burn")
	if !a.Insufficient || a.Firing || a.BudgetConsumed != 0 || a.RequiredValid != 348 || a.LongValid != 60 {
		t.Errorf("alert = %+v", a)
	}
	if !strings.Contains(a.Reason, "valid events") || !strings.HasPrefix(a.Explain(), "insufficient data:") {
		t.Errorf("reason=%q explain=%q", a.Reason, a.Explain())
	}
}

func TestShortHistoryReasonAndCoverage(t *testing.T) {
	now := t0.Add(110 * time.Second)
	var evs []event.Event
	for i := 0; i <= 110; i++ {
		evs = append(evs, ev(t0.Add(time.Duration(i)*time.Second), 0, 10))
	}
	a := byName(t, Evaluate(shipped(t), sloOf(99.9, 12*time.Hour), evs, now), "slow-burn")
	if !a.Insufficient || !strings.Contains(a.Reason, "history covers") {
		t.Errorf("alert = %+v", a)
	}
	near(t, "coverage", a.Coverage, 110.0/4320.0, 1e-9)
}

func TestCoverageThresholdIsInclusive(t *testing.T) {
	p, err := Parse([]byte(onePolicyYAML))
	if err != nil {
		t.Fatal(err)
	}
	s := sloOf(99.9, 720*time.Hour)
	now := t0.Add(3 * time.Hour)
	mk := func(n int) []event.Event {
		var evs []event.Event
		for i := 0; i < n; i++ {
			evs = append(evs, ev(now.Add(-time.Duration(i)*time.Minute), 2464, 2500))
		}
		return evs
	}
	if a := Evaluate(p, s, mk(49), now)[0]; a.Insufficient || !a.Firing { // 48 min of 60 = exactly 80%
		t.Errorf("exactly 80%% coverage is enough: %+v", a)
	}
	if a := Evaluate(p, s, mk(48), now)[0]; !a.Insufficient || a.Firing { // 47 min = 78.3%
		t.Errorf("78%% coverage is not enough: %+v", a)
	}
}

func TestLowTrafficOutageIsInsufficientNotAPage(t *testing.T) {
	var evs []event.Event
	for i := 0; i < 120; i++ { // one request a minute, every one of them fails
		evs = append(evs, ev(t0.Add(time.Duration(i)*time.Minute), 0, 1))
	}
	eps, err := Episodes(shipped(t), sloOf(99.9, 720*time.Hour), evs, t0.Add(60*time.Minute), t0.Add(119*time.Minute), time.Minute)
	if err != nil || len(eps) != 0 {
		t.Errorf("err=%v episodes=%+v, want none: too little traffic to judge", err, eps)
	}
}
