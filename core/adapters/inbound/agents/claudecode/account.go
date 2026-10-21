// account.go reads which Claude Code account an agent home is logged into, so
// the daemon can attribute a session's quota snapshot to one account (#2095).
//
// It is consent-gated by its OWN pseudo-adapter permission
// (account_permission.go), checked before any I/O. The identity file is read
// through an allowlist: a Go struct holding only the fields below, so the
// email address, display and organisation names and every token in the same
// file are decoded by nothing and kept by nothing.
package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"irrlicht/core/adapters/inbound/agents/accountfile"
	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// identityFile is the allowlist. Anything not named here is never decoded
// into memory this package holds.
type identityFile struct {
	OAuthAccount *struct {
		AccountUUID      string `json:"accountUuid"`
		OrganizationType string `json:"organizationType"`
	} `json:"oauthAccount"`
}

// accountFS is the file access seam (shared mechanics: accountfile); tests
// substitute it to count reads.
type accountFS = accountfile.FS

func osAccountFS() accountFS { return accountfile.OSFS() }

// AccountReader implements outbound.SessionAccountReader for Claude Code.
type AccountReader struct {
	granted func() bool
	fs      accountFS
	cache   *accountfile.Cache
}

var _ outbound.SessionAccountReader = (*AccountReader)(nil)

// NewAccountReader builds the production reader. granted is consulted before
// every read; nil means never granted.
func NewAccountReader(granted func() bool) *AccountReader {
	return newAccountReader(granted, osAccountFS())
}

func newAccountReader(granted func() bool, fs accountFS) *AccountReader {
	return &AccountReader{granted: granted, fs: fs, cache: accountfile.NewCache(fs)}
}

func (r *AccountReader) allowed() bool { return r != nil && r.granted != nil && r.granted() }

// identityPathFor returns the identity file of the home the transcript lives
// under, or "" when the transcript is outside the projects root the daemon
// watches. Default config dir (~/.claude) keeps its identity file as a
// SIBLING (~/.claude.json); a CLAUDE_CONFIG_DIR config dir keeps it inside
// ($CLAUDE_CONFIG_DIR/.claude.json), confirmed by the #2095 probe on claude
// 2.1.306.
func identityPathFor(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	root, err := agentpaths.AbsRoot(transcriptsDir())
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(root, filepath.Clean(transcriptPath))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if v := os.Getenv(configDirEnvVar); v != "" {
		if cleaned := filepath.Clean(v); filepath.IsAbs(cleaned) {
			return filepath.Join(cleaned, ".claude.json")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

// ReadHomeAccount implements outbound.SessionAccountReader.
func (r *AccountReader) ReadHomeAccount(s *session.SessionState) (outbound.HomeAccountObservation, bool) {
	if !r.allowed() || s == nil {
		return outbound.HomeAccountObservation{}, false
	}
	path := identityPathFor(s.TranscriptPath)
	if path == "" {
		return outbound.HomeAccountObservation{}, false
	}

	return r.cache.Lookup(path, parseIdentity)
}

// parseIdentity decodes the allowlisted fields of ~/.claude.json.
func parseIdentity(data []byte) (outbound.HomeAccountObservation, bool) {
	var f identityFile
	if err := json.Unmarshal(data, &f); err != nil {
		return outbound.HomeAccountObservation{}, false
	}
	obs := outbound.HomeAccountObservation{}
	if f.OAuthAccount != nil {
		obs.Ref = f.OAuthAccount.AccountUUID
		if obs.Ref != "" {
			obs.Product = f.OAuthAccount.OrganizationType
		}
	}
	return obs, true
}

// TranscriptStart implements outbound.SessionAccountReader: the first
// top-level "timestamp" in the first accountfile.HeadBytes of the transcript.
func (r *AccountReader) TranscriptStart(s *session.SessionState) int64 {
	if !r.allowed() || s == nil || identityPathFor(s.TranscriptPath) == "" {
		return 0
	}
	return accountfile.FirstTimestamp(r.fs, s.TranscriptPath)
}
