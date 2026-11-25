package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/sources"
)

// Coverage is the gap measurement of one input. Kind is "coverage" for sources with a known
// sample interval (probes, scrapes) and "silence" for access logs, where a quiet period and
// lost data look the same.
type Coverage struct {
	Kind           string  `json:"kind"`
	Samples        int     `json:"samples"`
	SpanSeconds    float64 `json:"span_seconds"`
	MissingSeconds float64 `json:"missing_seconds"`
	Ratio          float64 `json:"ratio"`
	Gaps           int     `json:"gaps"`
}

// Quality is what `ebg parse --stats-out` writes: how much of one input could be used.
type Quality struct {
	Source   string        `json:"source"`
	Stats    sources.Stats `json:"stats"`
	Coverage *Coverage     `json:"coverage,omitempty"`
}

// ReadQuality reads one statistics file strictly. Errors never echo the content.
func ReadQuality(r io.Reader) (Quality, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var q Quality
	if err := dec.Decode(&q); err != nil || dec.More() || q.Source == "" {
		return Quality{}, errors.New("not a valid parse statistics file (write one with `ebg parse --stats-out`)")
	}
	return q, nil
}

func dur(sec float64) time.Duration {
	return time.Duration(sec * float64(time.Second)).Round(time.Millisecond)
}

func writeQuality(b *strings.Builder, qs []Quality) {
	if len(qs) == 0 {
		b.WriteString("No parse statistics were supplied (use `ebg parse --stats-out FILE` and `--quality FILE`), so missing and malformed data was not measured.\n\n")
		return
	}
	for _, q := range qs {
		s := q.Stats
		bad := 0.0
		if s.Lines > 0 {
			bad = float64(s.Malformed) / float64(s.Lines) * 100
		}
		fmt.Fprintf(b, "- **%s**: %d lines read, %d malformed (%.1f%%), %d duplicates dropped, %d out of order, %d requests used, %d events made\n",
			q.Source, s.Lines, s.Malformed, bad, s.Duplicates, s.OutOfOrder, s.Requests, s.Events)
		if s.Scrapes > 0 {
			fmt.Fprintf(b, "  - prometheus: %d scrapes, %d counter resets handled, %d new series\n", s.Scrapes, s.Resets, s.NewSeries)
		}
		if c := q.Coverage; c != nil {
			share := 0.0
			if c.SpanSeconds > 0 {
				share = c.MissingSeconds / c.SpanSeconds * 100
			}
			if c.Kind == "silence" {
				fmt.Fprintf(b, "  - silence: %d samples over %s, quiet for %s (%.1f%% of the observed span), quiet periods: %d; access logs cannot tell a quiet period from lost data\n",
					c.Samples, dur(c.SpanSeconds), dur(c.MissingSeconds), share, c.Gaps)
			} else {
				fmt.Fprintf(b, "  - coverage: %d samples over %s, missing %s (%.1f%% of the observed span), gaps: %d\n",
					c.Samples, dur(c.SpanSeconds), dur(c.MissingSeconds), share, c.Gaps)
			}
		} else {
			b.WriteString("  - coverage: not measured (no --interval was given)\n")
		}
		for _, w := range s.Warnings {
			fmt.Fprintf(b, "  - warning: %s\n", w)
		}
		for _, e := range s.Errors {
			fmt.Fprintf(b, "  - parse error: %s\n", e)
		}
	}
	b.WriteString("\n")
}
