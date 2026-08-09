package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

// TestUnknownHookEventObserved wires this adapter's hook receiver into the
// shared issue #1364 contract: an event name the receiver does not recognize is
// counted per (adapter, name), reported once per distinct name, and still
// answered 2xx.
func TestUnknownHookEventObserved(t *testing.T) {
	contracttesting.AssertUnknownHookEventObserved(t, contracttesting.UnknownEventReceiver{
		Adapter: AdapterName,
		Root: func(t *testing.T) string {
			t.Helper()
			home := t.TempDir()
			t.Setenv(codexHomeEnvVar, home)
			root := filepath.Join(home, "sessions")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatalf("create sessions dir: %v", err)
			}
			return root
		},
		WriteTranscript: func(t *testing.T, dir string) string {
			t.Helper()
			return writeRolloutInto(t, dir, "sess-unknown-event")
		},
		New: func(t *testing.T, log outbound.Logger) contracttesting.UnknownEventReceiverUnderTest {
			t.Helper()
			target := &mockTarget{}
			return contracttesting.UnknownEventReceiverUnderTest{
				Handler:  NewHookHandlerWithConfiner(target, nil, log, TranscriptConfiner()),
				Observed: func() bool { return target.totalCalls() > 0 },
			}
		},
		PayloadFor: func(transcriptPath, event string) string {
			body, err := json.Marshal(codexHookPayload{
				TranscriptPath: transcriptPath,
				HookEventName:  event,
				ToolName:       "shell",
			})
			if err != nil {
				panic(err)
			}
			return string(body)
		},
		KnownEvent:   HookPermissionRequest,
		EndpointPath: HookEndpointPath,
	})
}
