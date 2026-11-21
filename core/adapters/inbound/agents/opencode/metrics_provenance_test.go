package opencode

import "testing"

// TestComputeMetrics_V1RecordsNoCostProvenance (#2061): the 1.x store fold's
// cost omits every "tool-calls" step, because applyStepFinishTokens builds no
// contribution for one. The fold must therefore not label that cost
// provider-reported, which the usage export would print as a vouched number.
func TestComputeMetrics_V1RecordsNoCostProvenance(t *testing.T) {
	db, dbPath := openTestOpencodeDB(t)
	const sid = "ses_test_cost_provenance"
	insertTestSession(t, db, sid, "/tmp/opencode-cost-provenance-test")
	insertTestMessage(t, db, testMessageRow{id: "msg_1", sid: sid, ts: 1000,
		data: `{"role":"assistant","time":{"created":1000},"model":{"providerID":"test","modelID":"test-model"}}`})
	for _, p := range []testPartRow{
		{id: "part_1", msgID: "msg_1", sid: sid, ts: 1100,
			data: `{"type":"step-finish","reason":"tool-calls","cost":0.01,"tokens":{"input":10,"output":2,"cache":{"read":0,"write":0}}}`},
		{id: "part_2", msgID: "msg_1", sid: sid, ts: 1200,
			data: `{"type":"step-finish","reason":"stop","cost":0.05,"tokens":{"input":20,"output":4,"cache":{"read":0,"write":0}}}`},
	} {
		insertTestPart(t, db, p)
	}

	m, err := ComputeMetrics(dbPath, sid)
	if err != nil || m == nil {
		t.Fatalf("ComputeMetrics = (%v, %v)", m, err)
	}
	// Lock: only the "stop" step's cost is summed.
	if m.EstimatedCostUSD != 0.05 {
		t.Fatalf("lock: EstimatedCostUSD = %v, want 0.05 (the tool-calls step's 0.01 is not counted)", m.EstimatedCostUSD)
	}
	if m.PricingSource != "" || len(m.UnpricedModels) != 0 {
		t.Errorf("provenance = (%q, %q), want none: a cost that omits the tool-calls steps must not be vouched for",
			m.PricingSource, m.UnpricedModels)
	}
}
