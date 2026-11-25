package event

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ReadJSONL reads events written one JSON object per line, the format `ebg parse` prints.
// This is our own format, so anything unexpected (unknown field, bad event, trailing text) is
// an error. Errors name the line number only, never the line.
func ReadJSONL(r io.Reader) ([]Event, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out []Event
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		var e Event
		if err := dec.Decode(&e); err != nil || dec.More() {
			return nil, fmt.Errorf("events line %d: not a valid event record", n)
		}
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("events line %d: %v", n, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read events near line %d: %w", n+1, err)
	}
	return out, nil
}
