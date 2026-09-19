package main

// Enrollment codes let a desktop join the relay with a URL instead of a
// pasted bearer token (#1963). Unlike phone pairing codes (push/codes.go,
// RAM only, since mint and redeem both happen inside the one serving
// relay), an enrollment code must be mintable by a separate, short-lived
// `irrlichtrelay enroll new` CLI process with no relay running at all, and
// visible to a serving relay's redeem route afterward — so its record set
// is a file, not RAM. Per the onetimecode.Store contract, each record holds
// the SHA-256 hash of the normalized code, never the plaintext, exactly as
// TokenRecord hashes bearer tokens (tokens.go). The serving relay re-reads
// the file by mtime on the redeem path, in the shape of
// authStore.reloadIfChanged (tokens.go:237): stat before read, so a write
// landing between the two calls cannot stamp stale content with a fresh
// mtime (see that function's comment for the race this ordering avoids).
// Mint races between the CLI and a serving relay inherit exactly the
// cross-process race authStore.writeMu's comment (tokens.go:157) already
// accepts for tokens; this store deliberately adds no cross-process locking
// either.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"irrlicht/core/pkg/onetimecode"
)

// enrollCodesFilename is the basename of the relay's enrollment code store,
// written under the relay data dir (docs/mobile-notifications-arc42.md
// §8.6).
const enrollCodesFilename = "enroll-codes.json"

// enrollCodeRecord is one outstanding enrollment code as persisted to disk:
// the SHA-256 hex of the normalized code (never the plaintext), the
// workspace and label it was minted with, and its expiry. ExpiresAt is
// time.Time (not a truncated unix-seconds int64 like TokenRecord.Created)
// so a code's expiry survives a round trip to the exact nanosecond the
// injected clock minted it at.
type enrollCodeRecord struct {
	Hash      string    `json:"hash"`
	Workspace string    `json:"workspace,omitempty"`
	Label     string    `json:"label,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

// hashEnrollCode returns the hex SHA-256 of a normalized code — the only
// form stored at rest, exactly as hashToken does for bearer tokens
// (tokens.go).
func hashEnrollCode(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// loadEnrollRecords reads the on-disk record set. A missing file is not an
// error — no code has been minted yet, matching loadTokens' treatment of a
// missing tokens.json.
func loadEnrollRecords(path string) ([]enrollCodeRecord, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []enrollCodeRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return recs, nil
}

// saveEnrollRecords writes the record set at mode 0600 (it holds code
// hashes) via a same-directory temp file and rename — the same atomic shape
// as saveTokens (tokens.go:70).
func saveEnrollRecords(path string, recs []enrollCodeRecord) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, enrollCodesFilename+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// fileEnrollStore is the onetimecode.Store backing enrollment: the file
// seam described in this file's header comment. It caches its last parse
// and re-reads only when the file's mtime has advanced, so a long-running
// relay does not re-parse the JSON on every redeem when nothing minted a
// new code since the last one.
type fileEnrollStore struct {
	path string

	mu     sync.Mutex
	cached []enrollCodeRecord
	mtime  time.Time
	loaded bool
}

// newFileEnrollStore builds a Store bound to path. Nothing is read until
// the first Load.
func newFileEnrollStore(path string) *fileEnrollStore {
	return &fileEnrollStore{path: path}
}

// Key hashes the normalized code — never the plaintext is written to disk.
func (f *fileEnrollStore) Key(normalized string) string {
	return hashEnrollCode(normalized)
}

func (f *fileEnrollStore) Load() ([]onetimecode.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Stat before read, matching authStore.reload's ordering: with
	// read-then-stat, a write landing between the two stamps old content
	// with the new mtime, and the cache below would then treat that stale
	// content as current until the file next changes.
	var mtime time.Time
	if fi, err := os.Stat(f.path); err == nil {
		mtime = fi.ModTime()
	}
	if !f.loaded || !mtime.Equal(f.mtime) {
		recs, err := loadEnrollRecords(f.path)
		if err != nil {
			return nil, err
		}
		f.cached = recs
		f.mtime = mtime
		f.loaded = true
	}
	out := make([]onetimecode.Record, len(f.cached))
	for i, r := range f.cached {
		out[i] = onetimecode.Record{Key: r.Hash, Workspace: r.Workspace, Label: r.Label, ExpiresAt: r.ExpiresAt}
	}
	return out, nil
}

func (f *fileEnrollStore) Save(records []onetimecode.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	recs := make([]enrollCodeRecord, len(records))
	for i, r := range records {
		recs[i] = enrollCodeRecord{Hash: r.Key, Workspace: r.Workspace, Label: r.Label, ExpiresAt: r.ExpiresAt}
	}
	if err := saveEnrollRecords(f.path, recs); err != nil {
		return err
	}
	f.cached = recs
	f.loaded = true
	if fi, err := os.Stat(f.path); err == nil {
		f.mtime = fi.ModTime()
	}
	return nil
}

// resolveEnrollCodesPath returns <data-dir>/enroll-codes.json.
func resolveEnrollCodesPath(dataDir string) string {
	return filepath.Join(dataDir, enrollCodesFilename)
}

// newEnrollManager builds the onetimecode.Manager over the file store at
// <dataDir>/enroll-codes.json. now is the injected clock (nil means
// time.Now). Works with no relay running — `enroll new` uses exactly this
// constructor, matching how `token issue` operates on the tokens file with
// no relay required (main.go's runToken).
func newEnrollManager(dataDir string, now func() time.Time) *onetimecode.Manager {
	return onetimecode.NewManager(now, newFileEnrollStore(resolveEnrollCodesPath(dataDir)))
}
