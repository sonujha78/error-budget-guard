package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/sonujha78/error-budget-guard/internal/verify"
)

// exitVerifyFailed means a recorded scenario did not match what was expected. It differs from
// the gate codes (0, 1, 2) and from a tool error (3).
const exitVerifyFailed = 4

func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sloPath := fs.String("slo", "configs/demo/slos.yaml", "SLO definition file")
	alertPath := fs.String("alerts", "configs/demo/alerts.yaml", "alert policy file")
	policyPath := fs.String("policy", "configs/policy.yaml", "error budget policy file")
	fail := func(err error) int {
		fmt.Fprintln(stderr, "ebg verify:", err)
		return exitError
	}
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() == 0 {
		return fail(errors.New("give at least one scenario directory"))
	}
	passed := 0
	for _, dir := range fs.Args() {
		res, err := verify.Run(dir, verify.Config{SLO: *sloPath, Alerts: *alertPath, Policy: *policyPath})
		if err != nil {
			return fail(fmt.Errorf("%s: %v", dir, err))
		}
		fmt.Fprintf(stdout, "scenario %s\n", filepath.Base(filepath.Clean(dir)))
		for _, c := range res.Checks {
			state := "PASS"
			if !c.Pass {
				state = "FAIL"
			}
			fmt.Fprintf(stdout, "  %s %s: %s\n", state, c.Name, c.Detail)
		}
		if res.OK() {
			passed++
		}
	}
	fmt.Fprintf(stdout, "summary: %d of %d scenarios passed\n", passed, fs.NArg())
	if passed != fs.NArg() {
		return exitVerifyFailed
	}
	return exitOK
}
