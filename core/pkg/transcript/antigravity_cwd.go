package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

// ExtractCWDFromAntigravityHistory resolves the real workspace cwd for an
// Antigravity transcript. agy writes its transcript at
//
//	<brainParent>/brain/<conv-id>/.system_generated/logs/transcript.jsonl
//
// but records only its sandbox scratch dir in the body (run_command Cwd), so the
// transcript can't yield the user's workspace. The real launch cwd lives in the
// sibling per-conversation index history.jsonl, keyed by conversationId:
//
//	{"conversationId":"<id>","workspace":"/abs/cwd",...}
//
// Resolving it lets the daemon bind the conv-id session to its agy process (the
// process cwd == the workspace) and label the project. Returns the LAST
// workspace seen for this conv-id (a session can move), or "" when the path
// isn't an antigravity transcript, the index is absent, or the conv-id has no
// workspace entry yet (the index is written lazily — the caller retries on the
// next activity refresh).
func ExtractCWDFromAntigravityHistory(transcriptPath string) string {
	convID, brainParent := antigravityConvAndBrainParent(transcriptPath)
	if convID == "" {
		return ""
	}
	f, err := os.Open(filepath.Join(brainParent, "history.jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()

	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e struct {
			ConversationID string `json:"conversationId"`
			Workspace      string `json:"workspace"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.ConversationID == convID && e.Workspace != "" {
			last = e.Workspace
		}
	}
	return last
}

// antigravityConvAndBrainParent splits an antigravity transcript path into its
// conversation ID and the directory that holds the brain/ store (history.jsonl's
// parent). Returns ("","") for any path that isn't the
// <brainParent>/brain/<conv-id>/.system_generated/logs/transcript.jsonl shape,
// so the resolver is a no-op for every other adapter's transcripts.
func antigravityConvAndBrainParent(p string) (convID, brainParent string) {
	if filepath.Base(p) != "transcript.jsonl" {
		return "", ""
	}
	logs := filepath.Dir(p)
	if filepath.Base(logs) != "logs" {
		return "", ""
	}
	sysGen := filepath.Dir(logs)
	if filepath.Base(sysGen) != ".system_generated" {
		return "", ""
	}
	convDir := filepath.Dir(sysGen)
	brain := filepath.Dir(convDir)
	if filepath.Base(brain) != "brain" {
		return "", ""
	}
	conv := filepath.Base(convDir)
	if conv == "" || conv == "." || conv == string(filepath.Separator) {
		return "", ""
	}
	return conv, filepath.Dir(brain)
}
