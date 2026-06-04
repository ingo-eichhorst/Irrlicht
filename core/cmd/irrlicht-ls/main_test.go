package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

func TestGoRunFromRepoRoot(t *testing.T) {
	repoRoot := repoRoot(t)
	homeDir := t.TempDir()
	instancesDir := filepath.Join(homeDir, "Library", "Application Support", "Irrlicht", "instances")
	if err := os.MkdirAll(instancesDir, 0o700); err != nil {
		t.Fatalf("mkdir instances dir: %v", err)
	}

	state := &session.SessionState{
		SessionID:   "root-run-12345678",
		State:       session.StateReady,
		ProjectName: "fixture-project",
		UpdatedAt:   time.Now().Add(-2 * time.Second).Unix(),
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(instancesDir, state.SessionID+".json"), data, 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	goPath := goEnv(t, "GOPATH")
	cmd := exec.Command("go", "run", "./core/cmd/irrlicht-ls")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		"GOCACHE="+t.TempDir(),
		"GOPATH="+goPath,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run ./core/cmd/irrlicht-ls from %s: %v\n%s", repoRoot, err, out)
	}

	got := string(out)
	for _, want := range []string{"ready", "fixture-project", "root-run"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output %q does not contain %q", got, want)
		}
	}

	// --format json emits the grouped tree.
	cmd = exec.Command("go", "run", "./core/cmd/irrlicht-ls", "--format", "json")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		"GOCACHE="+t.TempDir(),
		"GOPATH="+goPath,
	)
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("go run ./core/cmd/irrlicht-ls --format json: %v\n%s", err, out)
	}

	var payload struct {
		Groups []struct {
			Name   string `json:"name"`
			Agents []struct {
				SessionID string `json:"session_id"`
				State     string `json:"state"`
			} `json:"agents"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal json output: %v\n%s", err, out)
	}
	if len(payload.Groups) != 1 || payload.Groups[0].Name != "fixture-project" {
		t.Fatalf("json groups = %+v, want one group named fixture-project", payload.Groups)
	}
	if len(payload.Groups[0].Agents) != 1 || payload.Groups[0].Agents[0].SessionID != state.SessionID {
		t.Fatalf("json agents = %+v, want the fixture session", payload.Groups[0].Agents)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

func goEnv(t *testing.T, key string) string {
	t.Helper()
	cmd := exec.Command("go", "env", key)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go env %s: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}
