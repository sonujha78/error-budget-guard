package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var forbiddenStrict = []string{"203.0.113", "198.51.100", "tok_SECRET123", "user-4242", "SESS999", "alice"}

func reportBase(t *testing.T) []string {
	t.Helper()
	return []string{"report",
		"--slo", writeFile(t, "slos.yaml", demoSLO),
		"--alerts", "../../configs/alerts.yaml",
		"--policy", writeFile(t, "policy.yaml", testPolicy),
		"--events", "-"}
}

func TestReportOutage(t *testing.T) {
	code, out, errOut := runWith(events(150, 120), reportBase(t)...)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{
		"## Gate decision", "**BLOCK** (exit 2)", "fast-burn",
		"| demo-availability | 99.9% | 80.000% | 300.0 of 1.5 events (20000.0%) | 0.0% | overspent |",
		"| /pay | 300 | 100.0% |", "| 2xx | 300 | 100.0% |",
		"already used up", "No parse statistics were supplied",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not contain %q:\n%s", want, out)
		}
	}
}

func TestReportIsDeterministic(t *testing.T) {
	_, a, _ := runWith(events(150, 120), reportBase(t)...)
	_, b, _ := runWith(events(150, 120), reportBase(t)...)
	if a != b || a == "" {
		t.Error("two runs on the same data gave different reports")
	}
}

func TestReportShowsOverride(t *testing.T) {
	args := with(reportBase(t), "--override", "emergency hotfix for sev1", "--override-by", "alice")
	code, out, errOut := runWith(events(150, 120), args...)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"OVERRIDDEN by alice", "emergency hotfix for sev1", "(exit 0)"} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not contain %q:\n%s", want, out)
		}
	}
}

func TestOverrideReasonIsScrubbedInReportAndGate(t *testing.T) {
	reason := "emergency fix, token=tok_SECRET123 attached"
	for name, args := range map[string][]string{
		"report": with(reportBase(t), "--override", reason, "--override-by", "oncall"),
		"gate":   with(gateBase(t), "--override", reason, "--override-by", "oncall"),
	} {
		code, out, errOut := runWith(events(150, 120), args...)
		if name == "gate" && code != 0 {
			t.Fatalf("gate exit code %d, stderr: %s", code, errOut)
		}
		if strings.Contains(out, "tok_SECRET123") || !strings.Contains(out, "[redacted]") {
			t.Errorf("%s output must hide the token:\n%s", name, out)
		}
	}
}

func TestReportOutFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.md")
	code, out, errOut := runWith(events(150, 120), with(reportBase(t), "--out", path)...)
	if code != 0 || out != "" {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, out, errOut)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(data), "# Error budget report") {
		t.Errorf("report file: err=%v content=%q", err, string(data))
	}
}

func TestReportWithQualityFromParse(t *testing.T) {
	q := filepath.Join(t.TempDir(), "q.json")
	code, evs, errOut := runWith("", "parse", "--slo", "../../configs/slos.yaml", "--format", "probe",
		"--interval", "10s", "--stats-out", q, "../../testdata/probe_gap.log")
	if code != 0 {
		t.Fatalf("parse exit code %d, stderr: %s", code, errOut)
	}
	code, md, errOut := runWith(evs, "report", "--slo", "../../configs/slos.yaml", "--alerts", "../../configs/alerts.yaml",
		"--policy", writeFile(t, "policy.yaml", testPolicy), "--events", "-", "--quality", q)
	if code != 0 {
		t.Fatalf("report exit code %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"6 lines read", "missing 40s (40.0% of the observed span), gaps: 1"} {
		if !strings.Contains(md, want) {
			t.Errorf("report does not contain %q:\n%s", want, md)
		}
	}
}

func TestParseStatsOut(t *testing.T) {
	q := filepath.Join(t.TempDir(), "q.json")
	code, _, errOut := runWith("", "parse", "--slo", "../../configs/slos.yaml", "--format", "nginx", "--service", "checkout",
		"--stats-out", q, "../../testdata/nginx_sample.log")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	data, err := os.ReadFile(q)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `"source": "nginx-log"`) || !strings.Contains(s, `"malformed": 1`) || strings.Contains(s, `"coverage"`) {
		t.Errorf("stats file:\n%s", s)
	}
}

func TestNoSecretsAnywhere(t *testing.T) {
	policy := writeFile(t, "policy.yaml", testPolicy)
	for _, c := range []struct{ format, file string }{
		{"nginx", "nginx_sample.log"}, {"json", "json_sample.log"}, {"probe", "probe_sample.log"}, {"prom", "prom_sample.txt"},
	} {
		t.Run(c.format, func(t *testing.T) {
			q := filepath.Join(t.TempDir(), "q.json")
			code, evs, e1 := runWith("", "parse", "--slo", "../../configs/slos.yaml", "--format", c.format, "--service", "checkout",
				"--interval", "10s", "--stats-out", q, "../../testdata/"+c.file)
			if code != 0 {
				t.Fatalf("parse exit code %d, stderr: %s", code, e1)
			}
			code, md, e2 := runWith(evs, "report", "--slo", "../../configs/slos.yaml", "--alerts", "../../configs/alerts.yaml",
				"--policy", policy, "--events", "-", "--quality", q)
			if code != 0 {
				t.Fatalf("report exit code %d, stderr: %s", code, e2)
			}
			stats, _ := os.ReadFile(q)
			for _, out := range []string{evs, e1, md, e2, string(stats)} {
				for _, f := range forbiddenStrict {
					if strings.Contains(out, f) {
						t.Errorf("an output leaks %q", f)
					}
				}
			}
		})
	}
}

func TestReportToolErrorsExitWith3(t *testing.T) {
	base := reportBase(t)
	badQ := writeFile(t, "q.json", `{"source":"x","ip":"203.0.113.7"}`)
	cases := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"no events flag", []string{"report", "--slo", writeFile(t, "s.yaml", demoSLO)}, ""},
		{"empty events and no now", base, ""},
		{"garbage events", base, "not json"},
		{"bad now", with(base, "--now", "yesterday"), events(5, 5)},
		{"missing quality file", with(base, "--quality", "nope.json"), events(5, 5)},
		{"bad quality file", with(base, "--quality", badQ), events(5, 5)},
		{"override reason too short", with(base, "--override", "x", "--override-by", "oncall"), events(150, 120)},
		{"override without name", with(base, "--override", "a proper long reason"), events(150, 120)},
		{"stray argument", with(base, "stray"), events(5, 5)},
		{"help", []string{"report", "-h"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, _, _ := runWith(c.stdin, c.args...); code != 3 {
				t.Errorf("exit code %d, want 3", code)
			}
		})
	}
}
