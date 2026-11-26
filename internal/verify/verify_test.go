package verify

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const root = "../../"

func cfg() Config {
	return Config{SLO: root + "configs/demo/slos.yaml", Alerts: root + "configs/demo/alerts.yaml", Policy: root + "configs/policy.yaml"}
}

func scenarioDirs(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(root + "scenarios/*/expected.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, f := range files {
		dirs = append(dirs, filepath.Dir(f))
	}
	return dirs
}

func copyWith(t *testing.T, name string, mutate func(string) string) string {
	t.Helper()
	dst := t.TempDir()
	for _, f := range []string{"access.log", "truth.json", "expected.yaml"} {
		b, err := os.ReadFile(filepath.Join(root, "scenarios", name, f))
		if err != nil {
			t.Fatal(err)
		}
		if f == "expected.yaml" && mutate != nil {
			b = []byte(mutate(string(b)))
		}
		if err := os.WriteFile(filepath.Join(dst, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func hasFail(r Result, sub string) bool {
	for _, c := range r.Checks {
		if !c.Pass && strings.Contains(c.Name, sub) {
			return true
		}
	}
	return false
}

func TestParseSpecErrors(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"empty", "", "empty"},
		{"unknown field", "gate: 1\nbogus: 1\n", "bogus"},
		{"no gate", "forecast_divisor: 12\n", "gate is required"},
		{"gate 3", "gate: 3\n", "gate must be"},
		{"episode without slo", "gate: 1\nepisodes:\n  - {rule: fast-burn}\n", "slo and rule are required"},
		{"delay without onset", "gate: 1\nepisodes:\n  - {slo: a, rule: r, max_delay_s: 5}\n", "onset_seconds"},
		{"negative delay", "gate: 1\nonset_seconds: 10\nepisodes:\n  - {slo: a, rule: r, max_delay_s: -1}\n", "max_delay_s"},
		{"duplicate episode", "gate: 1\nepisodes:\n  - {slo: a, rule: r}\n  - {slo: a, rule: r}\n", "listed twice"},
		{"zero tolerance", "gate: 1\nforecast: {slo: a, true_seconds: 15, tolerance: 0}\n", "tolerance"},
		{"top without endpoint", "gate: 1\ntop_endpoint: {slo: a}\n", "top_endpoint"},
		{"negative divisor", "gate: 1\nforecast_divisor: -2\n", "forecast_divisor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseSpec([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err=%v, want substring %q", err, c.want)
			}
		})
	}
}

func TestShippedScenariosPass(t *testing.T) {
	dirs := scenarioDirs(t)
	if len(dirs) != 5 {
		t.Fatalf("found %d scenarios, want 5", len(dirs))
	}
	for _, d := range dirs {
		t.Run(filepath.Base(d), func(t *testing.T) {
			r, err := Run(d, cfg())
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Checks) < 2 {
				t.Errorf("only %d checks", len(r.Checks))
			}
			for _, c := range r.Checks {
				if !c.Pass {
					t.Errorf("FAIL %s: %s", c.Name, c.Detail)
				}
			}
		})
	}
}

// A verifier that cannot fail proves nothing. Each wrong expectation must be caught.
func TestWrongExpectationsAreCaught(t *testing.T) {
	cases := []struct {
		name, scenario, want string
		mutate               func(string) string
	}{
		{"wrong gate", "fast-burn", "gate decision", func(s string) string { return strings.Replace(s, "gate: 2", "gate: 0", 1) }},
		{"too tight delay", "fast-burn", "starts within", func(s string) string { return strings.ReplaceAll(s, "max_delay_s: 5}", "max_delay_s: 0.1}") }},
		{"wrong top endpoint", "fast-burn", "top error endpoint", func(s string) string { return strings.Replace(s, "endpoint: /api/pay}", "endpoint: /api/cart}", 1) }},
		{"unexpected alert", "fast-burn", "unexpected alert", func(s string) string {
			var keep []string
			for _, l := range strings.Split(s, "\n") {
				if !strings.Contains(l, "demo-availability, rule: medium-burn") {
					keep = append(keep, l)
				}
			}
			return strings.Join(keep, "\n")
		}},
		{"missing alert", "healthy", "appears", func(s string) string {
			return strings.Replace(s, "episodes: []", "episodes:\n  - {slo: demo-availability, rule: fast-burn}", 1)
		}},
		{"healthy is not insufficient", "healthy", "insufficient", func(s string) string { return s + "insufficient: true\n" }},
		{"wrong forecast", "slow-burn", "forecast", func(s string) string { return strings.Replace(s, "true_seconds: 15", "true_seconds: 30", 1) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Run(copyWith(t, c.scenario, c.mutate), cfg())
			if err != nil {
				t.Fatal(err)
			}
			if r.OK() || !hasFail(r, c.want) {
				t.Errorf("a wrong expectation was not caught (%q): %+v", c.want, r.Checks)
			}
		})
	}
}

func TestRunErrors(t *testing.T) {
	if _, err := Run(t.TempDir(), cfg()); err == nil {
		t.Error("an empty directory must be an error")
	}
	d := copyWith(t, "healthy", func(s string) string {
		return strings.Replace(s, "episodes: []", "episodes:\n  - {slo: nope, rule: fast-burn}", 1)
	})
	if _, err := Run(d, cfg()); err == nil || !strings.Contains(err.Error(), "unknown slo") {
		t.Errorf("err=%v, want unknown slo", err)
	}
}

func TestRunIsDeterministic(t *testing.T) {
	a, err1 := Run(root+"scenarios/fast-burn", cfg())
	b, err2 := Run(root+"scenarios/fast-burn", cfg())
	if err1 != nil || err2 != nil || !reflect.DeepEqual(a, b) {
		t.Error("two verifications of the same recording differ")
	}
}
