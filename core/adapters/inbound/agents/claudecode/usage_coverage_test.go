package claudecode

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// subagentFixture is a recorded foreground-subagent session whose parent
// transcript carries the finished child's own usage summary on the Agent
// call's tool result (toolUseResult.usage, line 26).
var subagentFixture = filepath.Join("..", "..", "..", "..", "..", "replaydata", "agents", "claudecode",
	"scenarios", "3-1_foreground-subagent", "recordings",
	"2026-09-06-23-53-54_irrlichd-0.6.2+388aba0.dirty", "transcript.jsonl")

// TestUsageCoverage_ParentIgnoresSubagentUsageSummary is the evidence behind
// usageCoverage's ExcludesSubagents (#2061). A LOCK, not a red-first test: it
// pins today's behaviour, that the parent's counts do not include the usage
// summary a finished subagent leaves on the parent's tool result. The child's
// own usage is in its own subagents/agent-*.jsonl transcript, which is a
// separate session.
//
// It parses the recording twice — as recorded, and with every count in every
// toolUseResult.usage multiplied by 1000 — and requires identical totals. It
// fails, rather than passes vacuously, when the recording carries no such
// summary.
//
// Two things in parser.go hold it: the summary sits on a user line with no
// requestId, and applyRequestIDContribution builds contributions only for
// lines with one; and findUsageMap does not look in toolUseResult. As run on
// 2026-11-21: a missing requestId falling back to the line's uuid left this
// test green, findUsageMap reading toolUseResult.usage first left it green,
// and both together turned it red. tools/mutate.sh takes one anchor, so
// tools/lib/herdr-usage-mutations_test.sh carries no fixture for it.
func TestUsageCoverage_ParentIgnoresSubagentUsageSummary(t *testing.T) {
	asRecorded, inflated, summaries := readWithInflatedSummaries(t, subagentFixture)
	if summaries == 0 {
		t.Fatal("the recording carries no toolUseResult.usage; this test would prove nothing")
	}

	base, grown := parentTotals(t, "recorded.jsonl", asRecorded), parentTotals(t, "inflated.jsonl", inflated)
	if base[2] == 0 {
		t.Fatalf("the parent counted no cache reads at all (%v); the fixture no longer exercises this", base)
	}
	if base != grown {
		t.Errorf("parent totals moved with the subagent's usage summary: as recorded %v, inflated %v — "+
			"the parent now counts its subagent, and usageCoverage's ExcludesSubagents is false", base, grown)
	}
	if !usageCoverage().ExcludesSubagents || !usageCoverage().ObservesAll() {
		t.Errorf("usageCoverage() = %+v, want all four token types and ExcludesSubagents", usageCoverage())
	}
}

// readWithInflatedSummaries reads a recording twice over — as recorded, and
// with every count in every toolUseResult.usage inflated — and counts the
// usage summaries it found.
func readWithInflatedSummaries(t *testing.T, path string) (asRecorded, inflated []byte, summaries int) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("the subagent recording must be readable: %v", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		asRecorded = append(append(asRecorded, line...), '\n')
		inflatedLine, found := inflateUsageSummary(t, line)
		if found {
			summaries++
		}
		inflated = append(append(inflated, inflatedLine...), '\n')
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return asRecorded, inflated, summaries
}

// inflateUsageSummary returns line with the counts in its
// toolUseResult.usage inflated, and whether it carried one; a line without
// one comes back as it was.
func inflateUsageSummary(t *testing.T, line []byte) ([]byte, bool) {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		t.Fatalf("recording line does not parse: %v", err)
	}
	tur, _ := raw["toolUseResult"].(map[string]any)
	usage, ok := tur["usage"].(map[string]any)
	if !ok {
		return line, false
	}
	inflateCounts(usage)
	out, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return out, true
}

// parentTotals parses body as a parent transcript and returns its token
// totals and cost.
func parentTotals(t *testing.T, name string, body []byte) [5]float64 {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := newCCTailer(p).TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess(%s): %v", name, err)
	}
	return [5]float64{float64(m.CumInputTokens), float64(m.CumOutputTokens),
		float64(m.CumCacheReadTokens), float64(m.CumCacheCreationTokens), m.EstimatedCostUSD}
}

// inflateCounts multiplies every number under v by 1000, nested maps and
// arrays included: the summary carries its counts twice, at the top level and
// in usage.iterations, and extractAnthropicContribution reads the iterations
// when they are present.
func inflateCounts(v any) {
	switch node := v.(type) {
	case map[string]any:
		inflateMembers(node)
	case []any:
		inflateElements(node)
	}
}

func inflateMembers(node map[string]any) {
	for k, child := range node {
		node[k] = inflated(child)
	}
}

func inflateElements(node []any) {
	for i, child := range node {
		node[i] = inflated(child)
	}
}

// inflated returns a number multiplied by 1000, or any other value as it is
// after inflating the numbers under it in place.
func inflated(v any) any {
	if n, ok := v.(float64); ok {
		return n * 1000
	}
	inflateCounts(v)
	return v
}
