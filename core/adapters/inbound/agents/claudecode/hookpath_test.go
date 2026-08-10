package claudecode

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"irrlicht/core/internal/contracttesting"
)

// claudeProjectsRoot relocates the config root to a temp dir and returns the
// declared transcript root inside it — CLAUDE_CONFIG_DIR moves the config root,
// and transcripts live in its projects/ subdirectory.
func claudeProjectsRoot(t *testing.T) string {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv(configDirEnvVar, cfg)
	return mkdirAllOrFail(t, filepath.Join(cfg, "projects"))
}

// writeContractTranscript writes a transcript the receiver will dispatch on.
// The session id is the filename stem, so the name has to look like one.
func writeContractTranscript(t *testing.T, dir string) string {
	t.Helper()
	return writeTranscriptFile(t, dir, transcriptStem)
}

// contractPayload renders this adapter's hook body for one event.
func contractPayload(transcriptPath, event string) string {
	body, err := json.Marshal(hookPayload{
		TranscriptPath: transcriptPath,
		HookEventName:  event,
		ToolName:       "Bash",
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// TestHookPathConfined wires this adapter's hook receiver into the shared issue
// #1361 contract: a caller-supplied transcript_path is confined to the
// adapter's declared transcript roots, with symlinks resolved first, and
// anything outside is refused loudly and counted.
func TestHookPathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, contracttesting.HookReceiver{
		Root:            claudeProjectsRoot,
		WriteTranscript: writeContractTranscript,
		TranscriptExt:   transcriptExt,
		New: func(t *testing.T) contracttesting.HookReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.HookReceiverUnderTest{
				Handler:  NewHookHandler(target, nil, nil, mockLogger{}),
				Observed: func() bool { return len(target.getCalls()) > 0 },
				ObservedPath: func() string {
					calls := target.getCalls()
					if len(calls) == 0 {
						return ""
					}
					return calls[len(calls)-1].transcriptPath
				},
			}
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

// TestStatuslinePathConfined runs the same #1361 contract against the
// statusline receiver. It sits on the same local, unauthenticated mux and takes
// the same caller-supplied path, and it is the receiver the first cut of this
// change forgot — which is the argument for a contract rather than a habit.
func TestStatuslinePathConfined(t *testing.T) {
	contracttesting.AssertHookPathConfined(t, contracttesting.HookReceiver{
		Root:            claudeProjectsRoot,
		WriteTranscript: writeContractTranscript,
		TranscriptExt:   transcriptExt,
		New: func(t *testing.T) contracttesting.HookReceiverUnderTest {
			t.Helper()
			target := &fakeRateLimitIngester{}
			return contracttesting.HookReceiverUnderTest{
				Handler:  NewStatuslineHandler(target, nil, silentLogger{}),
				Observed: func() bool { return len(target.calls) > 0 },
				ObservedPath: func() string {
					if len(target.calls) == 0 {
						return ""
					}
					return target.calls[len(target.calls)-1].path
				},
			}
		},
		// A rate_limits block is required or the handler acks and returns
		// before the ingest — the observation the contract keys on.
		PayloadFor: func(transcriptPath string) string {
			return `{"session_id":"abc","transcript_path":"` + transcriptPath +
				`","rate_limits":{"five_hour":{"used_percentage":47,"resets_at":1778761800}}}`
		},
		EndpointPath: StatuslineEndpointPath,
	})
}
