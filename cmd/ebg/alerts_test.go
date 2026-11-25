package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const demoSLO = `slos:
  - name: demo-availability
    service: demo
    sli: availability
    objective: 99.9
    window: 12h
    good:
      status_classes: [2xx, 3xx]
`

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// events makes one event per second, 10 requests each. From second badFrom on, every request fails.
func events(n, badFrom int) string {
	start := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	var b strings.Builder
	for i := 0; i < n; i++ {
		good := 10
		if i >= badFrom {
			good = 0
		}
		fmt.Fprintf(&b, `{"timestamp":%q,"service":"demo","sli":"availability","good":%d,"valid":10,"source":"test","label":"/pay|2xx"}`+"\n",
			start.Add(time.Duration(i)*time.Second).Format(time.RFC3339), good)
	}
	return b.String()
}

func lineWith(out, sub string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func with(base []string, extra ...string) []string {
	return append(append([]string{}, base...), extra...)
}

func TestAlertsCommandFiresOnOutage(t *testing.T) {
	slos := writeFile(t, "slos.yaml", demoSLO)
	args := []string{"alerts", "--slo", slos, "--alerts", "../../configs/alerts.yaml", "--events", "-"}
	code, out, errOut := runWith(events(150, 120), args...)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "alerts at 2026-11-24T10:02:29Z") || !strings.Contains(out, "scale=0.0166667") {
		t.Errorf("header is wrong:\n%s", out)
	}
	fast := lineWith(out, "fast-burn")
	if !strings.HasPrefix(fast, "  FIRING") || !strings.Contains(fast, "69.44%") {
		t.Errorf("fast-burn line = %q, want FIRING with 69.44%% of the budget used", fast)
	}
	_, out2, _ := runWith(events(150, 120), args...)
	if out != out2 {
		t.Error("two runs on the same data gave different output")
	}
}

func TestAlertsCommandHealthyTraffic(t *testing.T) {
	slos := writeFile(t, "slos.yaml", demoSLO)
	code, out, errOut := runWith(events(150, 150), "alerts", "--slo", slos, "--alerts", "../../configs/alerts.yaml", "--events", "-")
	if code != 0 || strings.Contains(out, "FIRING") {
		t.Errorf("code=%d stderr=%s\n%s", code, errOut, out)
	}
}

func TestAlertsCommandNoData(t *testing.T) {
	slos := writeFile(t, "slos.yaml", demoSLO)
	code, out, _ := runWith("", "alerts", "--slo", slos, "--alerts", "../../configs/alerts.yaml",
		"--events", "-", "--now", "2026-11-24T10:00:00Z")
	if code != 0 || !strings.Contains(out, "no data") || strings.Contains(out, "FIRING") {
		t.Errorf("code=%d\n%s", code, out)
	}
}

func TestAlertsCommandTimeline(t *testing.T) {
	slos := writeFile(t, "slos.yaml", demoSLO)
	code, out, errOut := runWith(events(150, 120), "alerts", "--slo", slos, "--alerts", "../../configs/alerts.yaml",
		"--events", "-", "--step", "1s")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "episodes") || !strings.Contains(out, "start 2026-11-24T10:02:00Z, still firing") {
		t.Errorf("timeline output:\n%s", out)
	}
}

func TestAlertsToolErrors(t *testing.T) {
	slos := writeFile(t, "slos.yaml", demoSLO)
	badPolicy := writeFile(t, "bad.yaml", "reference_window: 30d\nrules: []\n")
	base := []string{"alerts", "--slo", slos, "--alerts", "../../configs/alerts.yaml"}
	cases := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"no events flag", base, ""},
		{"empty events and no now", with(base, "--events", "-"), ""},
		{"bad now", with(base, "--events", "-", "--now", "yesterday"), events(5, 5)},
		{"negative step", with(base, "--events", "-", "--step", "-1s"), events(5, 5)},
		{"bad policy", []string{"alerts", "--slo", slos, "--alerts", badPolicy, "--events", "-"}, events(5, 5)},
		{"missing events file", with(base, "--events", "does-not-exist.jsonl"), ""},
		{"garbage events", with(base, "--events", "-"), "not json"},
		{"stray argument", with(base, "--events", "-", "stray"), events(5, 5)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, _, _ := runWith(c.stdin, c.args...); code != 3 {
				t.Errorf("exit code %d, want 3", code)
			}
		})
	}
}

func TestAlertsCommandInsufficientHistory(t *testing.T) {
	slos := writeFile(t, "slos.yaml", demoSLO)
	code, out, errOut := runWith(events(110, 80), "alerts", "--slo", slos, "--alerts", "../../configs/alerts.yaml", "--events", "-")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	if fast := lineWith(out, "fast-burn"); !strings.HasPrefix(fast, "  FIRING") {
		t.Errorf("fast-burn has enough data: %q", fast)
	}
	for _, rule := range []string{"medium-burn", "slow-burn"} {
		l := lineWith(out, rule)
		if !strings.HasPrefix(l, "  insufficient") || !strings.Contains(l, "history") {
			t.Errorf("%s line = %q, want insufficient because of history", rule, l)
		}
	}
	if !strings.Contains(out, "firing: 1 page, 0 ticket, 2 insufficient") {
		t.Errorf("summary line is wrong:\n%s", out)
	}
}
