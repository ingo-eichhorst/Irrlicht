package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/internal/contracttesting"
)

// TestHookPathConfined wires this adapter's hook receiver into the shared issue
// #1361 contract: a caller-supplied transcript_path is confined to the
// adapter's declared transcript roots, with symlinks resolved first, and
// anything outside is refused loudly and counted.
func TestHookPathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, contracttesting.HookReceiver{
		Root: func(t *testing.T) string {
			t.Helper()
			// CODEX_HOME relocates the agent home; rollouts live in its
			// sessions/ subdirectory, which is the declared root.
			home := t.TempDir()
			t.Setenv(codexHomeEnvVar, home)
			root := filepath.Join(home, "sessions")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatalf("create sessions dir: %v", err)
			}
			return root
		},
		New: func(t *testing.T) contracttesting.HookReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			confiner := TranscriptConfiner()
			return contracttesting.HookReceiverUnderTest{
				Handler:    NewHookHandlerWithConfiner(target, nil, mockLogger{}, confiner),
				Observed:   func() bool { return target.totalCalls() > 0 },
				Rejections: confiner.RejectionCount,
			}
		},
		NewProduction: func(t *testing.T) contracttesting.HookReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.HookReceiverUnderTest{
				Handler:  NewHookHandler(target, nil, mockLogger{}),
				Observed: func() bool { return target.totalCalls() > 0 },
			}
		},
		// Codex resolves the session id from the rollout's session_meta header,
		// so the decoy outside the tree carries one too — the only thing
		// keeping it from dispatching is confinement, not an unreadable file.
		WriteTranscript: func(t *testing.T, dir string) string {
			t.Helper()
			path := filepath.Join(dir, "rollout-2026-07-18T00-00-00-abcdefabcdef"+transcriptExt)
			meta := `{"type":"session_meta","payload":{"id":"sess-confine"}}` + "\n"
			if err := os.WriteFile(path, []byte(meta), 0o600); err != nil {
				t.Fatalf("write transcript: %v", err)
			}
			return path
		},
		PayloadFor: func(transcriptPath string) string {
			body, err := json.Marshal(codexHookPayload{
				TranscriptPath: transcriptPath,
				HookEventName:  HookPermissionRequest,
				ToolName:       "shell",
			})
			if err != nil {
				panic(err)
			}
			return string(body)
		},
		EndpointPath: HookEndpointPath,
	})
}
