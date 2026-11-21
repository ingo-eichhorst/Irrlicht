// Package claudesettings reconstructs which of Claude Code's settings files
// supplies one `env` variable for a session, in Claude Code's documented
// precedence (issue #2010 phase 1).
//
// Claude Code copies a settings file's `env` block into its own process
// environment and the documentation states that this value "overwrites the
// same variable exported in your shell". The #2010 probe measured it: with
// ANTHROPIC_BASE_URL=http://127.0.0.1:9 exported and a
// .claude/settings.local.json pointing at Z.ai, requests went to Z.ai. A
// reader of the process environment alone therefore reports a route the
// session never used. This package answers the question the environment
// cannot: which settings file, if any, sets the variable.
//
// The precedence and file locations come from Claude Code's settings
// documentation, read on 2026-11-21:
//
//   - https://code.claude.com/docs/en/settings#settings-precedence: "managed
//     settings, command line, project local, shared project, user", and "An
//     env block inside a settings file is an ordinary key and follows the
//     levels above".
//   - https://code.claude.com/docs/en/managed-settings: the system directory
//     per OS, the managed-settings.d drop-ins merged after managed-settings.json
//     "in alphabetical order" with env blocks merging "key by key", and the
//     ranking remote > MDM > files across managed sources.
//   - https://code.claude.com/docs/en/settings: in a git repository the local
//     file lives at the repository root (the main checkout's root for a
//     worktree), and a file an older version left in the starting directory
//     is still read, with "the root's value" winning.
//
// Not reconstructed, and named here so nobody reads silence as coverage:
// server-managed settings (fetched from claude.ai), the Windows registry,
// the local-file ownership exceptions, the workspace-trust gate on project
// and local env values in an interactive session, and a launch environment
// built by the Claude Desktop app, which the documentation says takes
// precedence over settings env values. A linked worktree of a BARE
// repository (commondir not ending in .git) is reported opaque, so its
// sessions' routes read unreadable: the documentation names the main
// checkout's root, and a bare repository has none, so where Claude Code
// keeps the local file there is unknown — no probe has looked.
package claudesettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Layer names, in Claude Code's precedence order (highest first).
const (
	LayerManaged = "managed"
	LayerCLI     = "cli"
	LayerLocal   = "local"
	LayerProject = "project"
	LayerUser    = "user"
)

// Source is one settings document Claude Code applies to a session.
type Source struct {
	// Layer is one of the Layer* constants.
	Layer string
	// Path is the settings file. Empty when Opaque.
	Path string
	// Opaque marks a source Claude Code applies whose content this package
	// cannot reconstruct: an MDM profile, a --settings argument, or a layer
	// whose file could not be located (no working directory, a relative
	// CLAUDE_CONFIG_DIR, an unreadable drop-in directory). A lookup that
	// reaches one before finding the variable cannot know the answer.
	Opaque bool
}

// Inputs is what locating a session's settings files needs.
type Inputs struct {
	// CWD is the session's primary working directory. Empty means unknown.
	CWD string
	// ConfigDir is the session process's CLAUDE_CONFIG_DIR. Empty means
	// unset, so the user file is under Home.
	ConfigDir string
	// ConfigDirUnknown says the process environment could not be read, so
	// whether CLAUDE_CONFIG_DIR relocates the user file is unknown.
	ConfigDirUnknown bool
	// Home is the user's home directory.
	Home string
	// ManagedDir is the system directory holding managed-settings.json and
	// managed-settings.d/ (ManagedDir() for the running OS).
	ManagedDir string
	// MDMProfiles are files whose existence means an MDM policy applies.
	MDMProfiles []string
	// CLIOpaque says the command line carries a settings flag
	// (FlagsTouchSettings), or that the command line could not be read.
	CLIOpaque bool
}

// Sources returns every settings document Claude Code applies for in,
// highest precedence first. Within the managed tier, drop-ins come before
// managed-settings.json in reverse alphabetical order, because the
// documentation merges them in alphabetical order after that file with a
// later file's value replacing an earlier one.
func Sources(in Inputs) []Source {
	var out []Source
	out = append(out, managedSources(in)...)
	if in.CLIOpaque {
		out = append(out, Source{Layer: LayerCLI, Opaque: true})
	}
	out = append(out, localSources(in)...)
	if in.CWD == "" {
		out = append(out, Source{Layer: LayerProject, Opaque: true})
	} else {
		out = append(out, Source{Layer: LayerProject, Path: filepath.Join(in.CWD, ".claude", "settings.json")})
	}
	out = append(out, userSource(in))
	return out
}

func managedSources(in Inputs) []Source {
	var out []Source
	for _, p := range in.MDMProfiles {
		if _, err := os.Stat(p); err == nil || !errors.Is(err, fs.ErrNotExist) {
			// Present, or could not tell: either way its content is unknown.
			out = append(out, Source{Layer: LayerManaged, Opaque: true})
			break
		}
	}
	if in.ManagedDir == "" {
		return out
	}
	dropins := filepath.Join(in.ManagedDir, "managed-settings.d")
	entries, err := os.ReadDir(dropins)
	switch {
	case err == nil:
		var names []string
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
				continue
			}
			names = append(names, name)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, name := range names {
			out = append(out, Source{Layer: LayerManaged, Path: filepath.Join(dropins, name)})
		}
	case !errors.Is(err, fs.ErrNotExist):
		out = append(out, Source{Layer: LayerManaged, Opaque: true})
	}
	return append(out, Source{Layer: LayerManaged, Path: filepath.Join(in.ManagedDir, "managed-settings.json")})
}

// localSources returns the project-local file(s). Inside a git repository
// whose root is not the home directory, Claude Code keeps the file at the
// repository root (the main checkout's root for a worktree) and still reads
// one left in the starting directory, the root's value winning.
func localSources(in Inputs) []Source {
	if in.CWD == "" {
		return []Source{{Layer: LayerLocal, Opaque: true}}
	}
	startFile := Source{Layer: LayerLocal, Path: filepath.Join(in.CWD, ".claude", "settings.local.json")}
	root, ok := repoRoot(in.CWD)
	if !ok {
		return []Source{{Layer: LayerLocal, Opaque: true}}
	}
	if root == "" || samePath(root, in.Home) {
		return []Source{startFile}
	}
	rootFile := Source{Layer: LayerLocal, Path: filepath.Join(root, ".claude", "settings.local.json")}
	if samePath(root, in.CWD) {
		return []Source{rootFile}
	}
	return []Source{rootFile, startFile}
}

func userSource(in Inputs) Source {
	switch {
	case in.ConfigDirUnknown:
		return Source{Layer: LayerUser, Opaque: true}
	case in.ConfigDir != "":
		if !filepath.IsAbs(in.ConfigDir) {
			return Source{Layer: LayerUser, Opaque: true}
		}
		return Source{Layer: LayerUser, Path: filepath.Join(in.ConfigDir, "settings.json")}
	case in.Home == "":
		return Source{Layer: LayerUser, Opaque: true}
	}
	return Source{Layer: LayerUser, Path: filepath.Join(in.Home, ".claude", "settings.json")}
}

func samePath(a, b string) bool {
	return a != "" && b != "" && filepath.Clean(a) == filepath.Clean(b)
}

// repoRoot finds the git repository containing dir, returning the main
// checkout's root for a linked worktree. ok is false when a .git entry was
// found but could not be resolved; root is "" outside any repository.
func repoRoot(dir string) (root string, ok bool) {
	for d := filepath.Clean(dir); ; {
		info, err := os.Lstat(filepath.Join(d, ".git"))
		if err == nil {
			if info.IsDir() {
				return d, true
			}
			return worktreeMainRoot(d)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", true
		}
		d = parent
	}
}

// worktreeMainRoot resolves a `.git` FILE. A linked worktree's gitdir holds
// a commondir file naming the main repository's .git directory; the main
// checkout is that directory's parent. A gitdir with no commondir (a
// submodule) is its own repository rooted at d.
func worktreeMainRoot(d string) (string, bool) {
	data, err := readBounded(filepath.Join(d, ".git"))
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(data))
	gitdir, found := strings.CutPrefix(line, "gitdir:")
	if !found {
		return "", false
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(d, gitdir)
	}
	common, err := readBounded(filepath.Join(gitdir, "commondir"))
	if errors.Is(err, fs.ErrNotExist) {
		return d, true
	}
	if err != nil {
		return "", false
	}
	commonDir := strings.TrimSpace(string(common))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitdir, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	if filepath.Base(commonDir) != ".git" {
		return "", false
	}
	return filepath.Dir(commonDir), true
}

// FlagsTouchSettings reports whether argv carries a flag that changes which
// settings apply: --settings (a JSON value or file inserted above the
// project files) or --setting-sources (which drops whole layers). argv is
// already in memory (the caller read the whole command line); only flag
// tokens are compared, and nothing from it — least of all a flag's value,
// which can be inline JSON holding a credential — is kept or returned.
func FlagsTouchSettings(argv []string) bool {
	for _, a := range argv {
		for _, flag := range []string{"--settings", "--setting-sources"} {
			if a == flag || strings.HasPrefix(a, flag+"=") {
				return true
			}
		}
	}
	return false
}

// maxSettingsBytes bounds one settings file read. Settings files are small
// JSON documents; a larger file is reported unreadable rather than buffered.
const maxSettingsBytes = 1 << 20

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- a Claude Code settings location from Sources, or the route file Sources produced
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSettingsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSettingsBytes {
		return nil, fmt.Errorf("claudesettings: %s exceeds %d bytes", path, maxSettingsBytes)
	}
	return data, nil
}

// ReadEnv returns the values of keys from the `env` block of the settings
// file at path, and only those keys: a key present with an empty string is
// returned as "", an absent key is left out.
//
// This is selective retention, not selective reading: the whole file is
// read into memory first, as EnvOf reads a process's whole environment
// buffer before filtering it (core/ports/outbound EnvOf). The `env` block's
// other values are decoded as raw bytes, never as strings, and nothing
// outside keys leaves this function, so a token sitting beside
// ANTHROPIC_BASE_URL is never returned to a caller that did not name it.
//
// A missing file returns an error satisfying errors.Is(err, fs.ErrNotExist).
// Any other failure — unreadable, oversized, not strict JSON (Claude Code's
// documentation: "a // comment or a trailing comma is a syntax error"), an
// `env` that is not an object, or a named key holding a non-string
// (including null) — is a different error: the file exists and its answer
// is unknown.
func ReadEnv(path string, keys map[string]struct{}) (map[string]string, error) {
	data, err := readBounded(path)
	if err != nil {
		return nil, err
	}
	// Decoded into maps, not a tagged struct: encoding/json matches a struct
	// tag case-insensitively, so `"Env"` or `"ENV"` — keys Claude Code does
	// not read — would otherwise be taken for the env block.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("claudesettings: %s is not a JSON settings object: %w", path, err)
	}
	out := make(map[string]string, len(keys))
	rawEnv, ok := doc["env"]
	if !ok {
		return out, nil
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rawEnv, &env); err != nil || env == nil {
		return nil, fmt.Errorf("claudesettings: %s: env is not an object", path)
	}
	for k := range keys {
		raw, ok := env[k]
		if !ok {
			continue
		}
		// A JSON null decodes into a string as "" without error, which would
		// read as Claude Code's documented "empty value cancels a shell
		// export". It is a schema error instead, so it is refused like any
		// other non-string.
		var v *string
		if err := json.Unmarshal(raw, &v); err != nil || v == nil {
			return nil, fmt.Errorf("claudesettings: %s: env.%s is not a string", path, k)
		}
		out[k] = *v
	}
	return out, nil
}

// Status values for a Lookup Result.
const (
	// StatusFound: a source sets the variable (possibly to "").
	StatusFound = "found"
	// StatusNotSet: every source was read and none sets the variable.
	StatusNotSet = "not-set"
	// StatusUnreadable: a source that outranks every hit exists but could
	// not be read or parsed.
	StatusUnreadable = "unreadable"
	// StatusOpaque: a source that outranks every hit cannot be
	// reconstructed (Source.Opaque).
	StatusOpaque = "opaque"
)

// Result is the outcome of Lookup.
type Result struct {
	Status string
	// Source is the deciding source: the one that set the variable, or the
	// one that stopped the walk. Zero for StatusNotSet.
	Source Source
	// Value is the variable's value for StatusFound.
	Value string
}

// Lookup walks sources highest precedence first and returns the first that
// decides key: one that sets it, or one whose content is unknown. A missing
// file is skipped. An unreadable or opaque source stops the walk, because a
// lower source's value is only the answer if every source above it is known
// not to set the variable.
func Lookup(sources []Source, key string) Result {
	keys := map[string]struct{}{key: {}}
	for _, s := range sources {
		if s.Opaque {
			return Result{Status: StatusOpaque, Source: s}
		}
		env, err := ReadEnv(s.Path, keys)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return Result{Status: StatusUnreadable, Source: s}
		}
		if v, ok := env[key]; ok {
			return Result{Status: StatusFound, Source: s, Value: v}
		}
	}
	return Result{Status: StatusNotSet}
}
