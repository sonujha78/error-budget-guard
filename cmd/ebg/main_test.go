package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sonujha78/error-budget-guard/internal/event"
)

var forbidden = []string{"203.0.113", "198.51.100", "tok_SECRET123", "user-4242", "SESS999", "alice", "bob", "4242"}

func runWith(stdin string, args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = run(args, strings.NewReader(stdin), &o, &e)
	return code, o.String(), e.String()
}

func TestParseNginxFile(t *testing.T) {
	code, out, errOut := runWith("", "parse", "--slo", "../../configs/slos.yaml",
		"--format", "nginx", "--service", "checkout", "../../testdata/nginx_sample.log")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 10 {
		t.Fatalf("got %d event lines, want 10", len(lines))
	}
	for _, l := range lines {
		var e event.Event
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("bad json line %q: %v", l, err)
		}
		if err := e.Validate(); err != nil {
			t.Errorf("invalid event: %v", err)
		}
	}
	if !strings.Contains(errOut, "malformed=1") {
		t.Errorf("stderr should report malformed=1, got: %s", errOut)
	}
	for _, f := range forbidden {
		if strings.Contains(out, f) || strings.Contains(errOut, f) {
			t.Errorf("output leaks %q", f)
		}
	}
}

func TestParseFromStdin(t *testing.T) {
	line := `203.0.113.7 - alice [23/Nov/2026:10:00:01 +0000] "GET /pay?token=tok_SECRET123 HTTP/1.1" 200 512 "-" "curl/8.5.0" rt=0.120` + "\n"
	code, out, errOut := runWith(line, "parse", "--slo", "../../configs/slos.yaml",
		"--format", "nginx", "--service", "checkout", "-")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	if n := len(strings.Split(strings.TrimSpace(out), "\n")); n != 2 {
		t.Errorf("got %d events, want 2", n)
	}
	for _, f := range forbidden {
		if strings.Contains(out, f) || strings.Contains(errOut, f) {
			t.Errorf("output leaks %q", f)
		}
	}
}

func TestToolErrorsExitWith3(t *testing.T) {
	cases := [][]string{
		{},
		{"nosuchcommand"},
		{"parse"},
		{"parse", "--slo", "../../configs/slos.yaml", "x.log"},
		{"parse", "--slo", "../../configs/slos.yaml", "--format", "nginx", "../../testdata/nginx_sample.log"},
		{"parse", "--slo", "../../configs/slos.yaml", "--format", "json", "does-not-exist.log"},
	}
	for _, args := range cases {
		if code, _, _ := runWith("", args...); code != 3 {
			t.Errorf("args %v: exit code %d, want 3", args, code)
		}
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := runWith("", "version")
	if code != 0 || !strings.HasPrefix(out, "ebg ") {
		t.Errorf("code=%d out=%q", code, out)
	}
}

func TestParsePrometheusFile(t *testing.T) {
	code, out, errOut := runWith("", "parse", "--slo", "../../configs/slos.yaml",
		"--format", "prom", "--service", "checkout", "../../testdata/prom_sample.txt")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	if n := len(strings.Split(strings.TrimSpace(out), "\n")); n != 10 {
		t.Errorf("got %d event lines, want 10", n)
	}
	for _, want := range []string{"scrapes=3", "resets=9", "malformed=1"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr should contain %q, got: %s", want, errOut)
		}
	}
	for _, f := range forbidden {
		if strings.Contains(out, f) || strings.Contains(errOut, f) {
			t.Errorf("output leaks %q", f)
		}
	}
}

func TestParseSkewShiftsTimestamps(t *testing.T) {
	line := `{"ts":"2026-11-23T10:00:03Z","service":"checkout","target":"/healthz","ok":true}` + "\n"
	code, out, errOut := runWith(line, "parse", "--slo", "../../configs/slos.yaml",
		"--format", "probe", "--skew", "probe=+3s", "-")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, `"timestamp":"2026-11-23T10:00:00Z"`) {
		t.Errorf("timestamp was not shifted back by 3s: %s", out)
	}
}

func TestParseSkewUnusedWarning(t *testing.T) {
	line := `{"ts":"2026-11-23T10:00:03Z","service":"checkout","target":"/healthz","ok":true}` + "\n"
	code, _, errOut := runWith(line, "parse", "--slo", "../../configs/slos.yaml",
		"--format", "probe", "--skew", "nginx-log=+3s", "-")
	if code != 0 || !strings.Contains(errOut, "matched no events") {
		t.Errorf("code=%d stderr=%s", code, errOut)
	}
}

func TestParseBadFlagValues(t *testing.T) {
	for _, extra := range [][]string{
		{"--skew", "probe=3"},
		{"--skew", "probe=+10m"},
		{"--interval", "-5s"},
	} {
		args := append([]string{"parse", "--slo", "../../configs/slos.yaml", "--format", "probe"}, extra...)
		args = append(args, "../../testdata/probe_sample.log")
		if code, _, _ := runWith("", args...); code != 3 {
			t.Errorf("%v: exit code %d, want 3", extra, code)
		}
	}
}

func TestParseProbeCoverage(t *testing.T) {
	code, _, errOut := runWith("", "parse", "--slo", "../../configs/slos.yaml",
		"--format", "probe", "--interval", "10s", "../../testdata/probe_gap.log")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"coverage: samples=6", "span=1m40s", "missing=40s", "ratio=0.600", "gaps=1"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr should contain %q, got: %s", want, errOut)
		}
	}
}

func TestParseDedupeLinesFlag(t *testing.T) {
	base := []string{"parse", "--slo", "../../configs/slos.yaml", "--format", "nginx", "--service", "checkout"}
	code, out, errOut := runWith("", append(base, "../../testdata/nginx_sample.log")...)
	if code != 0 || len(strings.Split(strings.TrimSpace(out), "\n")) != 10 || !strings.Contains(errOut, "duplicates=0") {
		t.Errorf("default: code=%d stderr=%s", code, errOut)
	}
	code, out, errOut = runWith("", append(base, "--dedupe-lines", "../../testdata/nginx_sample.log")...)
	if code != 0 || len(strings.Split(strings.TrimSpace(out), "\n")) != 8 || !strings.Contains(errOut, "duplicates=1") {
		t.Errorf("--dedupe-lines: code=%d stderr=%s", code, errOut)
	}
}
