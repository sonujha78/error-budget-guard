package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const testPolicy = `block_below_remaining: 0.10
warn_below_remaining: 0.25
on_insufficient: warn
min_history_coverage: 0.001
`

func gateBase(t *testing.T) []string {
	t.Helper()
	return []string{"gate",
		"--slo", writeFile(t, "slos.yaml", demoSLO),
		"--alerts", "../../configs/alerts.yaml",
		"--policy", writeFile(t, "policy.yaml", testPolicy),
		"--events", "-"}
}

// midBudgetEvents: 100 events of 1000 requests, 80 bad in the first one. 80% of the budget used.
func midBudgetEvents() string {
	start := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	var b strings.Builder
	for i := 0; i < 100; i++ {
		good := 1000
		if i == 0 {
			good = 920
		}
		fmt.Fprintf(&b, `{"timestamp":%q,"service":"demo","sli":"availability","good":%d,"valid":1000,"source":"test","label":"/pay|2xx"}`+"\n",
			start.Add(time.Duration(i)*time.Second).Format(time.RFC3339), good)
	}
	return b.String()
}

func TestGateBlocksOnPageAlert(t *testing.T) {
	code, out, errOut := runWith(events(150, 120), gateBase(t)...)
	if code != 2 {
		t.Fatalf("exit code %d, want 2, stderr: %s\n%s", code, errOut, out)
	}
	if !strings.Contains(out, "decision: BLOCK (exit 2)") || !strings.Contains(out, "fast-burn") {
		t.Errorf("output:\n%s", out)
	}
}

func TestGateAllowsHealthyTraffic(t *testing.T) {
	code, out, errOut := runWith(events(150, 150), gateBase(t)...)
	if code != 0 || !strings.Contains(out, "decision: ALLOW (exit 0)") || !strings.Contains(out, "100.0% of the error budget remains") {
		t.Errorf("code=%d stderr=%s\n%s", code, errOut, out)
	}
}

func TestGateWarnsOnInsufficientData(t *testing.T) {
	code, out, _ := runWith(events(5, 5), gateBase(t)...)
	if code != 1 || !strings.Contains(out, "decision: WARN (exit 1)") || !strings.Contains(out, "insufficient data, the budget cannot be judged") {
		t.Errorf("code=%d\n%s", code, out)
	}
}

func TestGateWarnsWhenBudgetIsLow(t *testing.T) {
	code, out, _ := runWith(midBudgetEvents(), gateBase(t)...)
	if code != 1 || !strings.Contains(out, "20.0% of the error budget remains") || !strings.Contains(out, "decision: WARN (exit 1)") {
		t.Errorf("code=%d\n%s", code, out)
	}
}

func TestGateOverrideIsVisibleInTheReport(t *testing.T) {
	args := with(gateBase(t), "--override", "emergency hotfix for sev1", "--override-by", "alice")
	code, out, errOut := runWith(events(150, 120), args...)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s\n%s", code, errOut, out)
	}
	for _, want := range []string{"BLOCK, OVERRIDDEN by alice", "emergency hotfix for sev1", "(exit 0)", "[block]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
}

func TestGateIsDeterministic(t *testing.T) {
	_, a, _ := runWith(events(150, 120), gateBase(t)...)
	_, b, _ := runWith(events(150, 120), gateBase(t)...)
	if a != b || a == "" {
		t.Error("two runs on the same data gave different output")
	}
}

func TestGateToolErrorsExitWith3(t *testing.T) {
	bad := writeFile(t, "bad.yaml", "block_below_remaining: 0\n")
	base := gateBase(t)
	cases := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"no events flag", []string{"gate", "--slo", writeFile(t, "s.yaml", demoSLO), "--alerts", "../../configs/alerts.yaml", "--policy", writeFile(t, "p.yaml", testPolicy)}, ""},
		{"bad policy", []string{"gate", "--slo", writeFile(t, "s.yaml", demoSLO), "--alerts", "../../configs/alerts.yaml", "--policy", bad, "--events", "-"}, events(5, 5)},
		{"missing policy file", with(base, "--policy", "nope.yaml"), events(5, 5)},
		{"empty events and no now", base, ""},
		{"bad now", with(base, "--now", "yesterday"), events(5, 5)},
		{"override reason too short", with(base, "--override", "x", "--override-by", "alice"), events(150, 120)},
		{"override without a name", with(base, "--override", "a proper long reason"), events(150, 120)},
		{"name without a reason", with(base, "--override-by", "alice"), events(150, 120)},
		{"stray argument", with(base, "stray"), events(5, 5)},
		{"help is not allow", []string{"gate", "-h"}, ""},
		{"garbage events", base, "not json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, _, _ := runWith(c.stdin, c.args...); code != 3 {
				t.Errorf("exit code %d, want 3", code)
			}
		})
	}
}
