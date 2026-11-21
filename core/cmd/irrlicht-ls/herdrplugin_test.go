package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The herdr plugin package (#2061), platforms/herdr/irrlicht-usage, run the
// way herdr runs it — its manifest's command under `sh`, with the environment
// herdr gives a plugin action — against a real irrlicht-ls build. herdr
// itself is not needed: what herdr contributes is the command line and the
// variables, and those are exactly what this drives.

var pluginDir = filepath.Join("..", "..", "..", "platforms", "herdr", "irrlicht-usage")

// manifestField returns the raw TOML value of `key = …` in the manifest. The
// manifest is flat enough that a line match is exact; a key it cannot find
// fails the test rather than reading as empty.
func manifestField(t *testing.T, manifest, key string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + ` = (.+)$`).FindStringSubmatch(manifest)
	if m == nil {
		t.Fatalf("herdr-plugin.toml has no %s line", key)
	}
	return m[1]
}

// TestHerdrPlugin_ManifestDeclaresTheUsageAction pins the parts of the
// manifest Factory and herdr depend on: the action id Factory looks for, a
// pane context, the herdr floor the socket contract was verified on, and the
// one platform irrlicht-ls ships for.
func TestHerdrPlugin_ManifestDeclaresTheUsageAction(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(pluginDir, "herdr-plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(raw)
	if strings.Count(manifest, "[[actions]]") != 1 {
		t.Errorf("want exactly one action, got %d", strings.Count(manifest, "[[actions]]"))
	}
	at := strings.Index(manifest, "[[actions]]")
	if at < 0 {
		t.Fatal("herdr-plugin.toml has no [[actions]] table")
	}
	plugin, action := manifest[:at], manifest[at:]
	for _, c := range []struct{ section, key, want string }{
		{action, "id", `"usage"`},
		{action, "contexts", `["pane"]`},
		{plugin, "min_herdr_version", `"0.8.0"`},
		{plugin, "platforms", `["macos"]`},
	} {
		if got := manifestField(t, c.section, c.key); got != c.want {
			t.Errorf("%s = %s, want %s", c.key, got, c.want)
		}
	}
}

// pluginCommand reads the action's command array from the manifest.
func pluginCommand(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(pluginDir, "herdr-plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	// A TOML array of basic strings is valid JSON.
	if err := json.Unmarshal([]byte(manifestField(t, string(raw), "command")), &argv); err != nil || len(argv) == 0 {
		t.Fatalf("command is not a string array: %v", err)
	}
	return argv
}

func buildIrrlichtLs(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "irrlicht-ls")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build irrlicht-ls: %v\n%s", err, out)
	}
	return bin
}

// runPlugin runs the manifest's command with herdr's plugin-action variables
// plus extra, and nothing else from this process's environment.
func runPlugin(t *testing.T, extra ...string) (code int, stdout, stderr string) {
	t.Helper()
	root, err := filepath.Abs(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	argv := pluginCommand(t)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append([]string{
		"HOME=" + t.TempDir(),
		"HERDR_PLUGIN_ROOT=" + root,
		"HERDR_PLUGIN_ID=irrlicht-usage",
		"HERDR_PLUGIN_ACTION_ID=usage",
		"HERDR_PANE_ID=w1:p1",
	}, extra...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", argv, err)
	}
	return code, out.String(), errb.String()
}

func TestHerdrPlugin_RunsIrrlichtLsForThePane(t *testing.T) {
	bin := buildIrrlichtLs(t)
	srv := serveFixture(t, filepath.Join("testdata", "herdr-usage", "complete", "sessions.json"))
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runPlugin(t, "IRRLICHT_LS="+bin, "IRRLICHT_BIND_ADDR="+u.Host, "PATH=/usr/bin:/bin")
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "herdr-usage", "complete", "want.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The golden was taken at a fixed clock; this run used the real one.
	sampled := regexp.MustCompile(`"sampled_at": "[^"]+"`)
	if got, wantS := sampled.ReplaceAllString(stdout, `"sampled_at": "X"`), sampled.ReplaceAllString(string(want), `"sampled_at": "X"`); got != wantS {
		t.Errorf("plugin output differs from complete/want.json\n--- got ---\n%s", stdout)
	}

	// The daemon gone — the demo's daemon-stop step: non-zero, one line.
	srv.Close()
	code, stdout, stderr = runPlugin(t, "IRRLICHT_LS="+bin, "IRRLICHT_BIND_ADDR="+u.Host, "PATH=/usr/bin:/bin")
	if code != 1 || stdout != "" || strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, "daemon unreachable") {
		t.Errorf("daemon gone: exit %d, stdout %q, stderr %q; want 1, nothing, one 'daemon unreachable' line", code, stdout, stderr)
	}
}

// TestHerdrPlugin_FindsIrrlichtLs pins usage.sh's lookup order: an explicit
// IRRLICHT_LS that is not executable is an error, never a cue to run another
// binary; without it, irrlicht-ls on PATH runs, with --herdr-usage.
func TestHerdrPlugin_FindsIrrlichtLs(t *testing.T) {
	code, stdout, stderr := runPlugin(t, "IRRLICHT_LS=/nonexistent/irrlicht-ls", "PATH=/usr/bin:/bin")
	if code != 127 || stdout != "" || strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, "IRRLICHT_LS=/nonexistent/irrlicht-ls") {
		t.Errorf("bad IRRLICHT_LS: exit %d, stdout %q, stderr %q; want 127, nothing, one line naming it", code, stdout, stderr)
	}

	stubDir := t.TempDir()
	stub := "#!/bin/sh\necho \"stub $*\"\nexit 3\n"
	if err := os.WriteFile(filepath.Join(stubDir, "irrlicht-ls"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = runPlugin(t, "PATH="+stubDir+":/usr/bin:/bin")
	if code != 3 || stdout != "stub --herdr-usage\n" {
		t.Errorf("irrlicht-ls on PATH: exit %d, stdout %q; want the stub's 3 and %q", code, stdout, "stub --herdr-usage\n")
	}
}
