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
