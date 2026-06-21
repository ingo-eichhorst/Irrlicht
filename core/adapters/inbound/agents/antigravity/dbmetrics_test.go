package antigravity

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"irrlicht/core/pkg/tailer"
)

// Compile-time proof the parser opts into transcript-path injection, the seam
// that lets turn_done locate the sibling conversation store.
var _ tailer.TranscriptPathAware = (*Parser)(nil)

// --- protobuf builders (mirror agy's gen_metadata wire shape) ----------------

func encodeVarint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func pbTag(field, wire int) []byte { return encodeVarint(uint64(field)<<3 | uint64(wire)) }

func pbVarint(field int, v uint64) []byte { return append(pbTag(field, 0), encodeVarint(v)...) }

func pbBytes(field int, b []byte) []byte {
	out := append(pbTag(field, 2), encodeVarint(uint64(len(b)))...)
	return append(out, b...)
}

// genBlob builds a gen_metadata blob carrying a context-token count and a
// canonical model id, in the nested shape the decoder expects:
//
//	#1 usage { #4 block { #5 = promptTokens }; #19 = model; #21 = displayName }
func genBlob(promptTokens uint64, model string) []byte {
	block := pbVarint(fieldPromptTokens, promptTokens)
	usage := pbBytes(fieldTokenBlock, block)
	usage = append(usage, pbBytes(fieldCanonicalModel, []byte(model))...)
	usage = append(usage, pbBytes(21, []byte("Gemini 3.1 Pro (Low)"))...) // display — ignored
	return pbBytes(fieldUsage, usage)
}

// writeStore creates a conversations/<conv>.db with the given gen_metadata rows
// (idx → blob) and returns the matching transcript path.
func writeStore(t *testing.T, root, conv string, rows map[int][]byte) string {
	t.Helper()
	convDir := filepath.Join(root, "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(convDir, conv+".db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE gen_metadata (idx integer primary key, data blob, size integer)`); err != nil {
		t.Fatal(err)
	}
	for idx, blob := range rows {
		if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data, size) VALUES (?, ?, ?)`, idx, blob, len(blob)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "brain", conv, ".system_generated", "logs", transcriptFilename)
}

// --- tests -------------------------------------------------------------------

func TestDecodeGenMetadata(t *testing.T) {
	got := decodeGenMetadata(genBlob(16353, "gemini-3.1-pro-low"))
	if got.contextTokens != 16353 {
		t.Errorf("contextTokens = %d, want 16353", got.contextTokens)
	}
	if got.model != "gemini-3.1-pro-low" {
		t.Errorf("model = %q, want gemini-3.1-pro-low", got.model)
	}

	// An unrecognized blob yields a zero result, not a panic.
	if got := decodeGenMetadata([]byte{0xff, 0x01, 0x02}); got.model != "" || got.contextTokens != 0 {
		t.Errorf("garbage blob: got %+v, want zero", got)
	}
}

func TestStorePathForTranscript(t *testing.T) {
	tp := "/home/u/.gemini/antigravity-cli/brain/abc-123/.system_generated/logs/transcript.jsonl"
	want := "/home/u/.gemini/antigravity-cli/conversations/abc-123.db"
	if got := storePathForTranscript(tp); got != want {
		t.Errorf("storePathForTranscript = %q, want %q", got, want)
	}
	// A non-transcript path yields "".
	if got := storePathForTranscript("/tmp/transcript_full.jsonl"); got != "" {
		t.Errorf("non-transcript path: got %q, want empty", got)
	}
}

func TestReadStoreModelTokens(t *testing.T) {
	root := t.TempDir()
	conv := "abc-123"
	// idx 1 (latest) is the current context; idx 0 is an earlier, smaller turn.
	tp := writeStore(t, root, conv, map[int][]byte{
		0: genBlob(8106, "gemini-3.1-pro-low"),
		1: genBlob(16353, "gemini-3.1-pro-low"),
	})

	var cache dbCache
	got, ok := readStoreModelTokens(tp, &cache)
	if !ok {
		t.Fatal("readStoreModelTokens: ok=false, want a successful read")
	}
	if got.contextTokens != 16353 {
		t.Errorf("contextTokens = %d, want 16353 (highest idx)", got.contextTokens)
	}
	if got.model != "gemini-3.1-pro-low" {
		t.Errorf("model = %q, want gemini-3.1-pro-low", got.model)
	}
	if !cache.ok {
		t.Error("cache should be populated after a read")
	}

	// Second read hits the cache (same mtime/size) and returns the same value.
	if got2, ok2 := readStoreModelTokens(tp, &cache); !ok2 || got2 != got {
		t.Errorf("cached read = (%+v, %v), want (%+v, true)", got2, ok2, got)
	}

	// A transcript whose store does not exist degrades to no-usage.
	missing := filepath.Join(root, "brain", "no-such-conv", ".system_generated", "logs", transcriptFilename)
	if _, ok := readStoreModelTokens(missing, &dbCache{}); ok {
		t.Error("absent store: ok=true, want false")
	}
}

// TestTurnDoneEnrichesFromStore is the end-to-end check: a turn_done event picks
// up tokens + the canonical model from the store, and the canonical id sticks so
// later working-phase events stay resolvable (the bar doesn't blink off).
func TestTurnDoneEnrichesFromStore(t *testing.T) {
	root := t.TempDir()
	conv := "abc-123"
	tp := writeStore(t, root, conv, map[int][]byte{0: genBlob(16353, "gemini-3.1-pro-low")})

	p := &Parser{}
	p.SetTranscriptPath(tp)

	// Boot the session on the display-form model — which alone does NOT resolve.
	p.ParseLine(line("USER_EXPLICIT", "USER_INPUT", 0,
		"<USER_REQUEST>\nhi\n</USER_REQUEST>\n<USER_SETTINGS_CHANGE>\nThe user changed setting `Model Selection` from None to Gemini 3.1 Pro (Low).\n</USER_SETTINGS_CHANGE>", nil))
	work := p.ParseLine(line("MODEL", "PLANNER_RESPONSE", 1, "I will run it.", runCommand("/repo")))
	if work.Tokens != nil {
		t.Error("a working assistant event must not carry tokens (store read is turn_done only)")
	}

	// Terminal line → turn_done → store enrichment.
	done := p.ParseLine(line("MODEL", "PLANNER_RESPONSE", 2, "", nil))
	if done.EventType != "turn_done" {
		t.Fatalf("got type=%q, want turn_done", done.EventType)
	}
	if done.Tokens == nil || done.Tokens.Total != 16353 {
		t.Fatalf("turn_done Tokens = %+v, want Total=16353 from the store", done.Tokens)
	}
	if done.ModelName != "gemini-3.1-pro-low" {
		t.Errorf("turn_done ModelName = %q, want the store's canonical gemini-3.1-pro-low", done.ModelName)
	}

	// No-flicker: the next turn's working event reports the canonical id too, so
	// ComputeContextUtilization keeps resolving the window between turns.
	p.ParseLine(line("USER_EXPLICIT", "USER_INPUT", 3, "<USER_REQUEST>\nmore\n</USER_REQUEST>", nil))
	next := p.ParseLine(line("MODEL", "PLANNER_RESPONSE", 4, "Working", runCommand("/repo")))
	if next.ModelName != "gemini-3.1-pro-low" {
		t.Errorf("post-store working event ModelName = %q, want gemini-3.1-pro-low (no flicker)", next.ModelName)
	}
}

// TestTurnDoneNoStoreIsHarmless proves the pre-#719 behavior survives when the
// store is absent: turn_done still fires, just without tokens.
func TestTurnDoneNoStoreIsHarmless(t *testing.T) {
	p := &Parser{}
	p.SetTranscriptPath("/no/such/brain/conv/.system_generated/logs/transcript.jsonl")
	done := p.ParseLine(line("MODEL", "PLANNER_RESPONSE", 0, "", nil))
	if done.EventType != "turn_done" {
		t.Fatalf("got type=%q, want turn_done", done.EventType)
	}
	if done.Tokens != nil {
		t.Errorf("absent store must leave Tokens nil, got %+v", done.Tokens)
	}
}
