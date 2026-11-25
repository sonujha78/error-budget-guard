package event

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestReadJSONLRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	in := []Event{base(), base()}
	in[1].Label = "/pay|5xx"
	in[1].Good = 0
	for _, e := range in {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	buf.WriteString("\n   \n")
	out, err := ReadJSONL(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d events, want 2 (blank lines are skipped)", len(out))
	}
	for i := range in {
		if !out[i].Timestamp.Equal(in[i].Timestamp) || out[i].Label != in[i].Label ||
			out[i].Good != in[i].Good || out[i].Valid != in[i].Valid || out[i].Service != in[i].Service {
			t.Errorf("event %d: got %+v, want %+v", i, out[i], in[i])
		}
	}
}

func TestReadJSONLErrors(t *testing.T) {
	const ok = `{"timestamp":"2026-11-23T10:00:00Z","service":"a","sli":"availability","good":1,"valid":1,"source":"s","label":"x"}`
	cases := []struct{ name, in, want string }{
		{"not json", "hello", "line 1"},
		{"second line is bad", ok + "\nbroken", "line 2"},
		{"unknown field", `{"timestamp":"2026-11-23T10:00:00Z","service":"a","sli":"availability","good":1,"valid":1,"source":"s","label":"x","ip":"203.0.113.7"}`, "line 1"},
		{"trailing garbage", ok + " xyz", "line 1"},
		{"invalid event", `{"timestamp":"2026-11-23T10:00:00Z","service":"a","sli":"availability","good":5,"valid":1,"source":"s","label":"x"}`, "must not exceed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ReadJSONL(strings.NewReader(c.in))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err=%v, want substring %q", err, c.want)
			}
			if strings.Contains(err.Error(), "203.0.113.7") {
				t.Error("error text echoes input content")
			}
		})
	}
}
