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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// transcriptHeadBytes bounds the read TranscriptStart makes of a transcript.
const transcriptHeadBytes = 64 << 10

// identityFile is the allowlist. Anything not named here is never decoded
// into memory this package holds.
type identityFile struct {
	OAuthAccount *struct {
		AccountUUID      string `json:"accountUuid"`
		OrganizationType string `json:"organizationType"`
	} `json:"oauthAccount"`
}

// accountFS is the file access seam; tests substitute it to count reads.
type accountFS struct {
	stat func(path string) (modUnix, size int64, err error)
	read func(path string) ([]byte, error)
	// readHead returns up to n leading bytes of path.
	readHead func(path string, n int) ([]byte, error)
}

func osAccountFS() accountFS {
	return accountFS{
		stat: func(p string) (int64, int64, error) {
			fi, err := os.Stat(p)
			if err != nil {
				return 0, 0, err
			}
			return fi.ModTime().Unix(), fi.Size(), nil
		},
		read: os.ReadFile,
		readHead: func(p string, n int) ([]byte, error) {
			f, err := os.Open(p)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			buf := make([]byte, n)
			m, err := io.ReadFull(f, buf)
			if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
				return nil, err
			}
			return buf[:m], nil
		},
	}
}

type cachedAccount struct {
	mod, size int64
	obs       outbound.HomeAccountObservation
}

// AccountReader implements outbound.SessionAccountReader for Claude Code.
type AccountReader struct {
	granted func() bool
	fs      accountFS

	mu    sync.Mutex
	cache map[string]cachedAccount
}

var _ outbound.SessionAccountReader = (*AccountReader)(nil)

// NewAccountReader builds the production reader. granted is consulted before
// every read; nil means never granted.
func NewAccountReader(granted func() bool) *AccountReader {
	return newAccountReader(granted, osAccountFS())
}

func newAccountReader(granted func() bool, fs accountFS) *AccountReader {
	return &AccountReader{granted: granted, fs: fs, cache: map[string]cachedAccount{}}
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

	r.mu.Lock()
	defer r.mu.Unlock()
	last, haveLast := r.cache[path]
	mod, size, err := r.fs.stat(path)
	if err != nil {
		return last.obs, haveLast
	}
	if haveLast && last.mod == mod && last.size == size {
		return last.obs, true
	}
	data, err := r.fs.read(path)
	if err != nil {
		return last.obs, haveLast
	}
	var f identityFile
	if err := json.Unmarshal(data, &f); err != nil {
		return last.obs, haveLast
	}
	obs := outbound.HomeAccountObservation{Home: path, ModTime: mod}
	if f.OAuthAccount != nil {
		obs.Ref = f.OAuthAccount.AccountUUID
		if obs.Ref != "" {
			obs.Product = f.OAuthAccount.OrganizationType
		}
	}
	r.cache[path] = cachedAccount{mod: mod, size: size, obs: obs}
	return obs, true
}

// TranscriptStart implements outbound.SessionAccountReader: the first
// top-level "timestamp" in the first transcriptHeadBytes of the transcript.
func (r *AccountReader) TranscriptStart(s *session.SessionState) int64 {
	if !r.allowed() || s == nil || identityPathFor(s.TranscriptPath) == "" {
		return 0
	}
	head, err := r.fs.readHead(s.TranscriptPath, transcriptHeadBytes)
	if err != nil {
		return 0
	}
	for _, line := range bytes.Split(head, []byte{'\n'}) {
		var ev struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Timestamp == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, ev.Timestamp); err == nil {
			return t.Unix()
		}
	}
	return 0
}
