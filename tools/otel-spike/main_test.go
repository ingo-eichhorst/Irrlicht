package main

import (
	"encoding/json"
	"testing"
	"time"
)

// TestAnyValue_IntEncodings pins the load-bearing gotcha from #1141: Claude
// Code's OTLP/JSON sends some intValue attributes as bare JSON numbers, not the
// spec-mandated strings. A receiver that types intValue as *string fails the
// whole payload and drops every attribute in the record.
func TestAnyValue_IntEncodings(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want string
	}{
		"int as bare number":   {`{"intValue": 6466}`, "6466"},
		"int as quoted string": {`{"intValue": "97815"}`, "97815"},
		"plain string value":   {`{"stringValue": "accept"}`, "accept"},
		"bool value":           {`{"boolValue": true}`, "true"},
		"double value":         {`{"doubleValue": 1.5}`, "1.5"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var v anyValue
			if err := json.Unmarshal([]byte(tc.raw), &v); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.raw, err)
			}
			if got := v.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTracesPayload_SurvivesNumberIntValue is the regression guard proper: a
// realistic span whose duration_ms is a bare number must still decode, with the
// string attribute alongside it intact.
func TestTracesPayload_SurvivesNumberIntValue(t *testing.T) {
	body := `{"resourceSpans":[{"scopeSpans":[{"spans":[{
		"name":"claude_code.tool.blocked_on_user",
		"endTimeUnixNano":"1784204668057209041",
		"attributes":[
			{"key":"duration_ms","value":{"intValue":6466}},
			{"key":"decision","value":{"stringValue":"accept"}}
		]}]}]}]}`
	var p tracesPayload
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("payload with number intValue must decode: %v", err)
	}
	spans := p.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	attrs := attrMap(spans[0].Attributes)
	if attrs["duration_ms"] != "6466" {
		t.Errorf("duration_ms = %q, want 6466", attrs["duration_ms"])
	}
	if attrs["decision"] != "accept" {
		t.Errorf("decision = %q, want accept", attrs["decision"])
	}
}

func TestLatencyMs(t *testing.T) {
	recv := time.Unix(0, 2_000_000_000) // 2.000s
	if got := latencyMs(recv, "1000000000"); got != 1000.0 {
		t.Errorf("latencyMs = %v, want 1000", got)
	}
	if got := latencyMs(recv, ""); got != -1 {
		t.Errorf("empty nano should be -1, got %v", got)
	}
	if got := latencyMs(recv, "not-a-number"); got != -1 {
		t.Errorf("bad nano should be -1, got %v", got)
	}
}
