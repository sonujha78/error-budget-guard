package sources

import "time"

// Options changes how Collect treats the input.
type Options struct {
	// DedupeLines also drops lines that are byte for byte identical when the log has no
	// request id. It is off by default: nginx timestamps have one second resolution, so
	// identical lines can be genuinely different requests, for example 500 failed retries
	// in one second. Dropping them would hide errors (false comfort).
	DedupeLines bool
}

// DedupRequests drops requests whose explicit request id was already seen. With byLine it
// also drops identical lines that have no id. The first occurrence is kept, order is kept.
func DedupRequests(reqs []Request, byLine bool) ([]Request, int) {
	seen := make(map[string]bool, len(reqs))
	out := make([]Request, 0, len(reqs))
	dups := 0
	for _, r := range reqs {
		key := r.DedupKey
		if key == "" && byLine {
			key = r.LineKey
		}
		if key != "" {
			if seen[key] {
				dups++
				continue
			}
			seen[key] = true
		}
		out = append(out, r)
	}
	return out, dups
}

// CountOutOfOrder counts requests that are older than one already seen earlier in the input.
// Events are sorted later, so this is information, not a repair.
func CountOutOfOrder(reqs []Request) int {
	n := 0
	var latest time.Time
	for _, r := range reqs {
		if r.Time.Before(latest) {
			n++
		} else {
			latest = r.Time
		}
	}
	return n
}

func requestTimes(reqs []Request) []time.Time {
	out := make([]time.Time, len(reqs))
	for i, r := range reqs {
		out[i] = r.Time
	}
	return out
}

func scrapeTimes(scrapes []Scrape) []time.Time {
	out := make([]time.Time, len(scrapes))
	for i, s := range scrapes {
		out[i] = s.Time
	}
	return out
}
