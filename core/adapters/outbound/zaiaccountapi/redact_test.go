package zaiaccountapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func decodeAny(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decoding %s: %v", b, err)
	}
	return v
}

// The committed fixture is a fixed point of the redaction: re-redacting it
// changes nothing, so the redaction neither drops an approved field nor
// rewrites an absent one — the first capture's missing five-hour
// nextResetTime must not come back as 0.
func TestRedactQuotaResponse_FixtureIsAFixedPoint(t *testing.T) {
	for _, c := range loadLiveProbe(t).Captures {
		again, err := RedactQuotaResponse(c.RedactedBody)
		if err != nil {
			t.Fatalf("%s: %v", c.At, err)
		}
		if !reflect.DeepEqual(decodeAny(t, again), decodeAny(t, c.RedactedBody)) {
			t.Errorf("%s: redacting the committed body changed it:\n got %s\nwant %s", c.At, again, c.RedactedBody)
		}
	}
	first := string(captureAt(t, probeNothingUsed))
	if strings.Count(first, "nextResetTime") != 1 {
		t.Errorf("the first capture must carry exactly one nextResetTime (the weekly window's); got %s", first)
	}
}

// Every field outside the allowlist is dropped, including ones a future
// response might add next to the limits.
func TestRedactQuotaResponse_DropsNonApprovedFields(t *testing.T) {
	raw := []byte(`{"code":200,"msg":"Operation successful","success":true,"user_email":"a@b.c",
		"data":{"level":"lite","account_id":"acct-1","api_key":"sk-1","limits":[
		{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":2,"remaining":1997,"percentage":1,"owner":"x"}]}}`)
	out, err := RedactQuotaResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"msg", "user_email", "a@b.c", "account_id", "acct-1", "api_key", "sk-1", "owner"} {
		if strings.Contains(string(out), forbidden) {
			t.Errorf("redacted output carries %q: %s", forbidden, out)
		}
	}
}

// The committed fixture records which raw top-level keys existed (names
// only) and holds nothing outside the allowlist.
func TestLiveProbeFixture_HoldsOnlyApprovedFields(t *testing.T) {
	for _, c := range loadLiveProbe(t).Captures {
		if c.StatusCode != 200 {
			t.Errorf("%s: status %d, want 200", c.At, c.StatusCode)
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(c.RedactedBody, &top); err != nil {
			t.Fatal(err)
		}
		for k := range top {
			if k != "code" && k != "success" && k != "data" {
				t.Errorf("%s: redacted body carries non-approved top-level key %q", c.At, k)
			}
		}
		if want := []string{"code", "data", "msg", "success"}; !reflect.DeepEqual(c.RawTopLevelKeys, want) {
			t.Errorf("%s: raw_top_level_keys = %v, want %v", c.At, c.RawTopLevelKeys, want)
		}
	}
}
