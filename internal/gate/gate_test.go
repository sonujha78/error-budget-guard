package gate

import (
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/alert"
	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/forecast"
)

const testYAML = `
block_below_remaining: 0.10
warn_below_remaining: 0.25
on_insufficient: warn
`

func policy(t *testing.T) *Policy {
	t.Helper()
	p, err := Parse([]byte(testYAML))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func healthy(name string, remaining float64) SLOState {
	return SLOState{Status: budget.Status{
		SLO: name, HasData: true, Window: 100 * time.Hour,
		BudgetTotal: 1000, BudgetUsed: 1000 * (1 - remaining), BudgetRemaining: 1000 * remaining,
	}}
}

func firing(severity string) alert.Alert {
	return alert.Alert{Rule: "fast-burn", Severity: severity, Firing: true, Threshold: 14.4}
}

func hasReason(d Decision, level, sub string) bool {
	for _, r := range d.Reasons {
		if r.Level == level && strings.Contains(r.Text, sub) {
			return true
		}
	}
	return false
}

func TestShippedPolicyLoads(t *testing.T) {
	p, err := Load("../../configs/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if p.BlockBelowRemaining != 0.10 || p.WarnBelowRemaining != 0.25 || p.OnInsufficient != Warn ||
		p.MinHistoryCoverage != 0.8 || p.WarnIfExhaustedWithin != 0.25 {
		t.Errorf("policy = %+v", p)
	}
}

func TestParsePolicyErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"empty file", "", "empty"},
		{"unknown field", testYAML + "blok: 1\n", "blok"},
		{"no block threshold", "warn_below_remaining: 0.25\non_insufficient: warn\n", "block_below_remaining"},
		{"block above 1", "block_below_remaining: 1.5\nwarn_below_remaining: 1.5\non_insufficient: warn\n", "block_below_remaining"},
		{"warn below block", "block_below_remaining: 0.3\nwarn_below_remaining: 0.2\non_insufficient: warn\n", "warn_below_remaining"},
		{"insufficient may not allow", "block_below_remaining: 0.1\nwarn_below_remaining: 0.25\non_insufficient: allow\n", "on_insufficient must be"},
		{"insufficient missing", "block_below_remaining: 0.1\nwarn_below_remaining: 0.25\n", "on_insufficient must be"},
		{"coverage too big", testYAML + "min_history_coverage: 2\n", "min_history_coverage"},
		{"negative forecast rule", testYAML + "warn_if_exhausted_within: -1\n", "warn_if_exhausted_within"},
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

func TestDecideByRemainingBudget(t *testing.T) {
	cases := []struct {
		name      string
		remaining float64
		want      string
	}{
		{"plenty", 0.60, Allow},
		{"exactly the warn threshold is fine", 0.25, Allow},
		{"just under the warn threshold", 0.2499, Warn},
		{"exactly the block threshold only warns", 0.10, Warn},
		{"just under the block threshold", 0.0999, Block},
		{"nothing left", 0, Block},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(policy(t), []SLOState{healthy("a", c.remaining)})
			if d.Level != c.want || d.Effective != c.want {
				t.Errorf("level=%s effective=%s, want %s", d.Level, d.Effective, c.want)
			}
			if !hasReason(d, c.want, "% of the error budget remains") {
				t.Errorf("the budget number must be among the reasons: %+v", d.Reasons)
			}
		})
	}
}

func TestOverspentBlocks(t *testing.T) {
	st := healthy("a", 0)
	st.Status.Overspent, st.Status.BudgetUsed = true, 2000
	if d := Decide(policy(t), []SLOState{st}); d.Level != Block {
		t.Errorf("level = %s", d.Level)
	}
}

func TestPageAlertBlocksEvenWithPlentyOfBudget(t *testing.T) {
	st := healthy("a", 0.9)
	st.Alerts = []alert.Alert{firing(alert.SeverityPage)}
	d := Decide(policy(t), []SLOState{st})
	if d.Level != Block || !hasReason(d, Block, "fast-burn") {
		t.Errorf("decision = %+v", d)
	}
}

func TestTicketAlertWarns(t *testing.T) {
	st := healthy("a", 0.9)
	st.Alerts = []alert.Alert{firing(alert.SeverityTicket)}
	if d := Decide(policy(t), []SLOState{st}); d.Level != Warn {
		t.Errorf("level = %s", d.Level)
	}
}

func TestInsufficientAlertRuleIsOnlyInformation(t *testing.T) {
	st := healthy("a", 0.9)
	st.Alerts = []alert.Alert{{Rule: "slow-burn", Severity: alert.SeverityTicket, Insufficient: true, Reason: "history covers 2.5%"}}
	d := Decide(policy(t), []SLOState{st})
	if d.Level != Allow || !hasReason(d, Allow, "history covers") {
		t.Errorf("decision = %+v", d)
	}
}

func TestInsufficientBudgetFollowsPolicyAndIgnoresNumbers(t *testing.T) {
	st := SLOState{Status: budget.Status{SLO: "a", Insufficient: true, Reason: "only 50 valid events"}} // numbers are 0
	d := Decide(policy(t), []SLOState{st})
	if d.Level != Warn || !hasReason(d, Warn, "only 50 valid events") {
		t.Errorf("decision = %+v", d)
	}
	p := policy(t)
	p.OnInsufficient = Block
	if d := Decide(p, []SLOState{st}); d.Level != Block {
		t.Errorf("on_insufficient: block gave %s", d.Level)
	}
}

func TestNoSLOsIsInsufficient(t *testing.T) {
	d := Decide(policy(t), nil)
	if d.Level != Warn || len(d.Reasons) == 0 {
		t.Errorf("decision = %+v", d)
	}
}

func TestWorstSLOWins(t *testing.T) {
	d := Decide(policy(t), []SLOState{healthy("good-one", 0.9), healthy("bad-one", 0.05)})
	if d.Level != Block {
		t.Errorf("level = %s", d.Level)
	}
	if !hasReason(d, Allow, "") || !hasReason(d, Block, "") {
		t.Errorf("both SLOs must be explained: %+v", d.Reasons)
	}
	names := ""
	for _, r := range d.Reasons {
		names += r.SLO + ","
	}
	if !strings.Contains(names, "good-one") || !strings.Contains(names, "bad-one") {
		t.Errorf("reason SLOs = %s", names)
	}
}

func TestForecastWarning(t *testing.T) {
	st := healthy("a", 0.5)
	st.Forecast = forecast.Forecast{Known: true, Burn: 4, RemainingFraction: 0.5, TimeToExhaustion: 10 * time.Hour}
	p := policy(t)
	if d := Decide(p, []SLOState{st}); d.Level != Allow {
		t.Errorf("rule is off by default (0): %s", d.Level)
	}
	p.WarnIfExhaustedWithin = 0.25 // 25% of 100h = 25h, and 10h is less
	d := Decide(p, []SLOState{st})
	if d.Level != Warn || !hasReason(d, Warn, "forecast") {
		t.Errorf("decision = %+v", d)
	}
	st.Forecast.TimeToExhaustion = 40 * time.Hour
	if d := Decide(p, []SLOState{st}); d.Level != Allow {
		t.Errorf("40h left is more than 25%% of the window: %s", d.Level)
	}
}

func TestExitCodes(t *testing.T) {
	cases := map[string]int{Allow: 0, Warn: 1, Block: 2, "": 2, "garbage": 2}
	for level, want := range cases {
		if got := (Decision{Effective: level}).ExitCode(); got != want {
			t.Errorf("ExitCode(%q) = %d, want %d", level, got, want)
		}
	}
}

func TestOverrideKeepsTheTruthVisible(t *testing.T) {
	d := Decide(policy(t), []SLOState{healthy("a", 0.05)})
	o, err := d.WithOverride("emergency   hotfix\nfor sev1 outage", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !o.Overridden || o.Level != Block || o.Effective != Allow || o.ExitCode() != 0 {
		t.Errorf("override = %+v", o)
	}
	if o.OverrideReason != "emergency hotfix for sev1 outage" || o.OverrideBy != "alice" {
		t.Errorf("reason and name must be kept, with whitespace tidied: %q by %q", o.OverrideReason, o.OverrideBy)
	}
	s := o.Summary()
	for _, want := range []string{"BLOCK", "OVERRIDDEN", "alice", "emergency hotfix for sev1 outage", "ALLOW"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q does not contain %q", s, want)
		}
	}
	if len(o.Reasons) != len(d.Reasons) {
		t.Error("an override must not remove the original reasons")
	}
	if d.Overridden || d.Effective != Block {
		t.Error("the original decision was modified")
	}
}

func TestOverrideNeedsReasonAndName(t *testing.T) {
	d := Decide(policy(t), []SLOState{healthy("a", 0.05)})
	for _, c := range []struct{ reason, by string }{
		{"", "alice"}, {"   ", "alice"}, {"x", "alice"}, {"too short", "alice"}, {"a proper long reason", ""}, {"a proper long reason", "  "},
	} {
		if _, err := d.WithOverride(c.reason, c.by); err == nil {
			t.Errorf("reason %q by %q must be refused", c.reason, c.by)
		}
	}
	// validated even when there is nothing to override
	ok := Decide(policy(t), []SLOState{healthy("a", 0.9)})
	if _, err := ok.WithOverride("x", "alice"); err == nil {
		t.Error("a bad override must be refused even for an allow decision")
	}
	o, err := ok.WithOverride("a proper long reason", "alice")
	if err != nil || o.Overridden || o.Level != Allow {
		t.Errorf("an allow decision has nothing to override: %+v err=%v", o, err)
	}
}
