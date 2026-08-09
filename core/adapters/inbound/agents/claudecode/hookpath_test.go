package claudecode

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
			// CLAUDE_CONFIG_DIR relocates the config root; transcripts live in
			// its projects/ subdirectory, which is the declared root.
			cfg := t.TempDir()
			t.Setenv(configDirEnvVar, cfg)
			root := filepath.Join(cfg, "projects")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatalf("create projects dir: %v", err)
			}
			return root
		},
		New: func(t *testing.T) contracttesting.HookReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			confiner := TranscriptConfiner()
			return contracttesting.HookReceiverUnderTest{
				Handler:    NewHookHandlerWithConfiner(target, nil, nil, mockLogger{}, confiner),
				Observed:   func() bool { return len(target.getCalls()) > 0 },
				Rejections: confiner.RejectionCount,
			}
		},
		// The session id is the filename stem, so the name has to look like a
		// session id for a dispatch to happen at all.
		WriteTranscript: func(t *testing.T, dir string) string {
			t.Helper()
			path := filepath.Join(dir, transcriptStem+transcriptExt)
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				t.Fatalf("write transcript: %v", err)
			}
			return path
		},
		PayloadFor: func(transcriptPath string) string {
			body, err := json.Marshal(hookPayload{
				TranscriptPath: transcriptPath,
				HookEventName:  HookPermissionRequest,
				ToolName:       "Bash",
			})
			if err != nil {
				panic(err)
			}
			return string(body)
		},
		EndpointPath: HookEndpointPath,
	})
}
