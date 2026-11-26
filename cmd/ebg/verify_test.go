package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func verifyBase() []string {
	return []string{"verify", "--slo", "../../configs/demo/slos.yaml",
		"--alerts", "../../configs/demo/alerts.yaml", "--policy", "../../configs/policy.yaml"}
}

func TestVerifyAllShippedScenarios(t *testing.T) {
	dirs, _ := filepath.Glob("../../scenarios/*")
	code, out, errOut := runWith("", append(verifyBase(), dirs...)...)
	if code != 0 || !strings.Contains(out, "summary: 5 of 5 scenarios passed") || strings.Contains(out, "FAIL") {
		t.Fatalf("code=%d stderr=%s\n%s", code, errOut, out)
	}
}

func TestVerifyFailsWithExit4(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"access.log", "truth.json", "expected.yaml"} {
		b, err := os.ReadFile("../../scenarios/fast-burn/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if f == "expected.yaml" {
			b = []byte(strings.Replace(string(b), "gate: 2", "gate: 0", 1))
		}
		os.WriteFile(filepath.Join(dir, f), b, 0o600)
	}
	code, out, _ := runWith("", append(verifyBase(), dir)...)
	if code != 4 || !strings.Contains(out, "FAIL gate decision") || !strings.Contains(out, "summary: 0 of 1 scenarios passed") {
		t.Errorf("code=%d\n%s", code, out)
	}
}

func TestVerifyToolErrorsExitWith3(t *testing.T) {
	for _, args := range [][]string{
		verifyBase(),
		append(verifyBase(), "does-not-exist"),
		{"verify", "-h"},
	} {
		if code, _, _ := runWith("", args...); code != 3 {
			t.Errorf("args %v: exit code %d, want 3", args, code)
		}
	}
}
