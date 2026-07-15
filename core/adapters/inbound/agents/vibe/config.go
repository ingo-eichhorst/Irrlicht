package vibe

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// configFilename is the user-level config Vibe reads from its home directory.
// Verified against the installed package source at v2.19.1 —
// vibe/core/config/harness_files/_harness_manager.py:55 resolves the user
// config as VIBE_HOME/"config.toml", so this file moves with $VIBE_HOME and
// the two relocation seams compose.
const configFilename = "config.toml"

// saveDirKey is the config key that relocates the session root outright, and
// saveDirTable is the table it lives under.
//
// Verified against vibe/core/config/models.py at v2.19.1:
//
//	class SessionLoggingConfig(BaseSettings):
//	    save_dir: str = ""
//	    ...
//	    @field_validator("save_dir", mode="before")
//	    def set_default_save_dir(cls, v): return v or str(SESSION_LOG_DIR.path)
//
// so an unset save_dir falls back to $VIBE_HOME/logs/session, and a set one
// REPLACES that root rather than layering under it. vibe/core/session/
// session_logger.py:56-76 then writes <save_dir>/<prefix>_<ts>_<id>/ holding
// the messages.jsonl + meta.json pair this adapter watches, so the configured
// value really is the directory sessions appear under.
//
// Reading this matters more than a niche knob would: vibe/cli/cli.py:133-141
// (bootstrap_config_files) writes a default config.toml on first run with
// save_dir already spelled out as a resolved absolute path, so the key sits in
// every installation's config inviting an edit — and an edited value makes
// every Vibe session invisible to a daemon watching the default root (#1115).
const (
	saveDirTable = "session_logging"
	saveDirKey   = "save_dir"
)

// vibeHomeDir returns Vibe's home directory — $VIBE_HOME when it holds an
// absolute path, else ~/.vibe — mirroring vibe/core/paths/_vibe_home.py.
// Unlike upstream it does not expand "~" or resolve relative values; that
// narrowing matches agentpaths.FromEnv, which sessionsDir uses for the same
// env var.
func vibeHomeDir() (string, error) {
	if v := os.Getenv(vibeHomeEnvVar); v != "" {
		if cleaned := filepath.Clean(v); filepath.IsAbs(cleaned) {
			return cleaned, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, defaultHomeDirName), nil
}

// configuredSaveDir returns the absolute session root set by
// [session_logging].save_dir in Vibe's user config, or "" when it is unset,
// absent, unreadable, or not resolvable by the daemon. "" means "fall back to
// the default root", so every failure here lands on today's behavior rather
// than on a worse one.
//
// Deliberately limited to the USER config ($VIBE_HOME/config.toml). Vibe also
// honors a project config at <cwd>/.vibe/config.toml, which REPLACES the user
// file wholesale rather than merging with it (_harness_manager.py:48-57), but
// it is keyed to the directory vibe was launched from and gated on that
// directory being trusted. The daemon has no such cwd and FilesUnderRoot
// watches one root, so a project-level save_dir is unresolvable here rather
// than merely unimplemented. Same for VIBE_SESSION_LOGGING, which lives in
// vibe's process environment, not the daemon's.
func configuredSaveDir() string {
	home, err := vibeHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, configFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		// An absent or unreadable config is the ordinary case (Vibe writes one
		// only once it has run), not something to report.
		return ""
	}
	v := parseSaveDir(string(data))
	if v == "" {
		return ""
	}
	return resolveSaveDir(v, path)
}

// resolveSaveDir turns a raw save_dir value into an absolute path, or ""
// when it does not resolve.
//
// Upstream applies Path(v).expanduser().resolve() (models.py:65-68), so it
// accepts three shapes. "~"-relative is expanded here because a config file is
// not shell-expanded — Vibe itself does the expansion, so "~/logs" is a
// working value rather than the misconfiguration the same text would be in an
// env var. A relative value is rejected: .resolve() anchors it to the
// directory vibe was launched from, so it names a different directory per
// launch, and the daemon cannot know which.
func resolveSaveDir(v, configPath string) string {
	if v == "~" || strings.HasPrefix(v, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		v = filepath.Join(home, strings.TrimPrefix(v, "~"))
	}
	cleaned := filepath.Clean(v)
	if !filepath.IsAbs(cleaned) {
		log.Printf("vibe: ignoring [%s].%s=%q in %s — only absolute and ~-relative paths resolve; "+
			"vibe expands a relative value against the directory it was launched from, which the daemon cannot know",
			saveDirTable, saveDirKey, v, configPath)
		return ""
	}
	return cleaned
}

// parseSaveDir extracts [session_logging].save_dir from TOML content,
// returning "" when the key is absent or its value is not a single-line
// string.
//
// This is a deliberately minimal reader rather than a TOML dependency: one
// key, from one table, of one type. The repo carries no TOML library and
// hand-scans the only other agent config it reads (~/.codex/config.toml, in
// core/pkg/tailer/tailer_config.go), so a dependency for a single key would be
// new weight for no reach. It tracks tables, which that scanner does not —
// save_dir is meaningless without knowing it sits under [session_logging].
// Anything it does not understand yields "", which falls back to the default
// root.
func parseSaveDir(content string) string {
	inTable := false
	for raw := range strings.SplitSeq(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inTable = tableName(line) == saveDirTable
			continue
		}
		if !inTable {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != saveDirKey {
			continue
		}
		return tomlString(strings.TrimSpace(value))
	}
	return ""
}

// tableName returns the name of a TOML table header, or "" for anything that
// is not a plain [table] — including an [[array-of-tables]] header, which
// never names our target.
func tableName(line string) string {
	if strings.HasPrefix(line, "[[") {
		return ""
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(line[1:end])
}

// tomlString decodes a TOML string value, ignoring any trailing comment, and
// returns "" for anything that is not a basic ("...") or literal ('...')
// single-line string. Multi-line and bare values yield "".
func tomlString(v string) string {
	if v == "" {
		return ""
	}
	switch v[0] {
	case '"':
		// strconv.Unquote covers the escapes a path realistically carries
		// (\\ and \"); a value it rejects falls back to the default root.
		if s, err := strconv.Unquote(quotedSpan(v, '"')); err == nil {
			return s
		}
	case '\'':
		// A literal string has no escapes — its content is verbatim.
		if span := quotedSpan(v, '\''); len(span) >= 2 {
			return span[1 : len(span)-1]
		}
	}
	return ""
}

// quotedSpan returns v's leading quoted run including both quotes, or "" when
// the quote is never closed. Escapes are honored only for basic strings, which
// is where TOML defines them.
func quotedSpan(v string, quote byte) string {
	escaped := quote == '"'
	for i := 1; i < len(v); i++ {
		if escaped && v[i] == '\\' {
			i++
			continue
		}
		if v[i] == quote {
			return v[:i+1]
		}
	}
	return ""
}
