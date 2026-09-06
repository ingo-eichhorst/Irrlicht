package desktopdriver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cell 2-3 failed after Desktop re-rendered the composer. WaitComposer cached
// one prompt hierarchy. SetPrompt later sent that stale hierarchy directly to
// the helper and failed its value_equals check. A prompt action must resolve
// the prompt from the current tree before it changes the value.
func TestSetPromptResolvesTheCurrentPromptAfterAComposerRerender(t *testing.T) {
	root := t.TempDir()
	requests := filepath.Join(root, "requests.jsonl")
	fresh := controlsComposerElements("workspace")
	for index := range fresh {
		if fresh[index].Description == "Prompt" {
			fresh[index].Hierarchy = append(fresh[index].Hierarchy, "AXGroup", "AXTextArea")
		}
	}
	inspect, err := json.Marshal(helperResponse{OK: true, Elements: fresh})
	if err != nil {
		t.Fatal(err)
	}
	refused, err := json.Marshal(helperResponse{OK: false, Error: &struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: "postcondition_failed", Message: "value_equals was not observed"}})
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "helper")
	script := "#!/bin/sh\n" +
		"req=$(cat)\n" +
		"printf '%s\\n' \"$req\" >> '" + requests + "'\n" +
		"case \"$req\" in\n" +
		"  *'\"command\":\"inspect\"'*) printf '%s\\n' '" + string(inspect) + "' ;;\n" +
		"  *'AXGroup'*) printf '%s\\n' '{\"ok\":true}' ;;\n" +
		"  *) printf '%s\\n' '" + string(refused) + "'; exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewLiveRuntime(LiveOptions{
		Home: root, HelperPath: helper, DaemonAddress: "127.0.0.1:1",
		RecordingDirectory: filepath.Join(root, "recordings"),
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	runtime.frontDesktop = func(context.Context) error { return nil }
	runtime.workspace = "/repo/workspace"

	if err := runtime.SetPrompt(context.Background(), OwnedSession{}, "hello"); err != nil {
		t.Fatalf("SetPrompt() error = %v; the current prompt control was available", err)
	}
	raw, err := os.ReadFile(requests)
	if err != nil {
		t.Fatalf("read helper requests: %v", err)
	}
	if !strings.Contains(string(raw), `"command":"inspect"`) {
		t.Fatal("SetPrompt() did not inspect the current tree before changing the prompt")
	}
}

// A later prompt must stay inside the session that this run owns. A selected
// foreign conversation is a hard refusal. The driver must not set any value.
func TestSetPromptRefusesASelectedForeignConversation(t *testing.T) {
	elements := append(controlsComposerElements("workspace"),
		sessionMenuElement("Someone else's session", 29))
	setCalls := 0
	err := setPrompt(
		context.Background(),
		"/repo/workspace",
		OwnedSession{Registry: RegistrySession{SessionID: "local-owned", Title: "Owned session"}},
		"hello",
		func(context.Context) error { return nil },
		func(context.Context) ([]helperElement, error) { return elements, nil },
		func(context.Context, helperSelector, string) error {
			setCalls++
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "owns") {
		t.Fatalf("setPrompt() error = %v, want the selected-session ownership refusal", err)
	}
	if setCalls != 0 {
		t.Fatalf("setValue was called %d times for a foreign conversation", setCalls)
	}
}
