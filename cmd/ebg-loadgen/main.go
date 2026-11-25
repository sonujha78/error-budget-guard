// Command ebg-loadgen runs a scenario against the demo and writes its ground truth.
// Every secret in the requests below is fake and exists only to prove that the guard removes it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/demo/scenario"
)

type truth struct {
	Scenario        string           `json:"scenario"`
	StartUnix       float64          `json:"start_unix"`
	EndUnix         float64          `json:"end_unix"`
	RPS             float64          `json:"rps"`
	Planned         scenario.Summary `json:"planned"`
	Sent            int              `json:"sent"`
	ObservedStatus  map[string]int   `json:"observed_status"`
	TransportErrors int              `json:"transport_errors"`
}

func unix(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }

func send(c *http.Client, target string, i int, r scenario.Req) (int, error) {
	url := fmt.Sprintf("%s%s?token=tok_DEMO_SECRET_%04d&user_id=uid-9f31c0", target, r.Path, i%1000)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-User-Id", "uid-9f31c0")
	req.Header.Set("Cookie", "session=SESS999")
	if r.Fail {
		req.Header.Set("X-Demo-Fail", "1")
	}
	if r.DelayMS > 0 {
		req.Header.Set("X-Demo-Delay-Ms", strconv.Itoa(r.DelayMS))
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, nil
}

func main() { os.Exit(run()) }

func run() int {
	file := flag.String("scenario", "", "scenario YAML file (required)")
	target := flag.String("target", "http://ebg-nginx:80", "base URL of the system under test")
	out := flag.String("out", "", "file for the ground truth JSON (required)")
	flag.Parse()
	if *file == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "ebg-loadgen: --scenario and --out are required")
		return 2
	}
	s, err := scenario.Load(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ebg-loadgen:", err)
		return 2
	}
	plan := s.Plan()
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 100}}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		observed = map[string]int{}
		failed   int
	)
	start := time.Now()
	for i, r := range plan {
		time.Sleep(time.Until(start.Add(r.At)))
		wg.Add(1)
		go func(i int, r scenario.Req) {
			defer wg.Done()
			code, err := send(client, *target, i, r)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				return
			}
			observed[strconv.Itoa(code)]++
		}(i, r)
	}
	wg.Wait()
	end := time.Now()
	t := truth{
		Scenario: s.Name, StartUnix: unix(start), EndUnix: unix(end), RPS: s.RPS,
		Planned: s.Summarize(), Sent: len(plan), ObservedStatus: observed, TransportErrors: failed,
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err == nil {
		err = os.WriteFile(*out, append(data, '\n'), 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ebg-loadgen:", err)
		return 1
	}
	fmt.Printf("scenario %s: sent %d in %.1fs, status %v, transport errors %d\n", s.Name, len(plan), end.Sub(start).Seconds(), observed, failed)
	return 0
}
