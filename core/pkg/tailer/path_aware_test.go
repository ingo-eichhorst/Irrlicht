package tailer

import "testing"

// pathAwareParser records the transcript path injected at construction.
type pathAwareParser struct{ got string }

func (p *pathAwareParser) ParseLine(raw map[string]interface{}) *ParsedEvent { return nil }
func (p *pathAwareParser) SetTranscriptPath(path string)                     { p.got = path }

// NewTranscriptTailer must hand its path to parsers implementing
// TranscriptPathAware (sidecar-reading adapters, #599) — at construction,
// before any line is parsed.
func TestNewTranscriptTailer_InjectsPathIntoAwareParser(t *testing.T) {
	p := &pathAwareParser{}
	NewTranscriptTailer("/tmp/x.jsonl", p, "kiro-cli")
	if p.got != "/tmp/x.jsonl" {
		t.Errorf("injected path = %q, want /tmp/x.jsonl", p.got)
	}
}
