package transcript

import (
	"encoding/json"
	"os"
	"strings"
)

// ExtractCWDFromSidecar extracts the working directory from a metadata
// sidecar living next to a JSONL transcript: <base>.jsonl → <base>.json
// with a top-level "cwd" string field. Kiro CLI writes this shape
// (~/.kiro/sessions/cli/<uuid>.json); adapters whose transcript lines
// carry no cwd resolve it from here. Returns "" when there is no sidecar
// or no cwd field.
//
// The sidecar holds the agent's full conversation state and can grow
// large, so the decoder walks top-level keys and stops at "cwd" (the
// second key in practice) instead of unmarshalling the whole document.
func ExtractCWDFromSidecar(transcriptPath string) string {
	if !strings.HasSuffix(transcriptPath, ".jsonl") {
		return ""
	}
	sidecar := strings.TrimSuffix(transcriptPath, ".jsonl") + ".json"
	f, err := os.Open(sidecar)
	if err != nil {
		return ""
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	tok, err := dec.Token()
	if err != nil {
		return ""
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return ""
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return ""
		}
		key, _ := keyTok.(string)
		if key == "cwd" {
			valTok, err := dec.Token()
			if err != nil {
				return ""
			}
			cwd, _ := valTok.(string)
			return cwd
		}
		if err := skipJSONValue(dec); err != nil {
			return ""
		}
	}
	return ""
}

// skipJSONValue consumes one complete JSON value (scalar, object, or
// array) from the decoder.
func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || (d != '{' && d != '[') {
		return nil // scalar — already consumed
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}
