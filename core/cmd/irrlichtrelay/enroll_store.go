package main

// Enrollment codes let a desktop join the relay with a URL instead of a
// pasted bearer token (#1963). Unlike phone pairing codes (push/codes.go,
// RAM only, since mint and redeem both happen inside the one serving
// relay), an enrollment code must be mintable by a separate, short-lived
// `irrlichtrelay enroll new` CLI process with no relay running at all, and
// visible to a serving relay's redeem route afterward — so its record set
// is a file, not RAM, always read fresh from disk (no cache: see
// fileEnrollStore's own doc for why one was not worth carrying). Per the
// onetimecode.Store contract, each record holds the SHA-256 hash of the
// normalized code, never the plaintext, exactly as TokenRecord hashes
// bearer tokens (tokens.go). Mint races between the CLI and a serving relay
// inherit exactly the cross-process race authStore.writeMu's comment
// (tokens.go:160-166) already accepts for tokens; this store deliberately
// adds no cross-process locking either.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// form stored at rest. Same scheme as hashToken (tokens.go), same package:
// calls it directly rather than reimplementing sha256+hex, so the hashing
// scheme has one definition.
func hashEnrollCode(normalized string) string {
	return hashToken(normalized)
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
// seam described in this file's header comment. It carries no cache: Load
// always reads fresh from disk. A cache was not worth what it would cost to
// keep coherent — Manager already calls Load exactly once per Mint/Redeem,
// and Redeem checks the rolling failure window *before* ever calling Load,
// so the ceiling is FailureLimit (10) reads per FailureWindow (a minute)
// over a file holding at most MaxOutstanding (32) small records. An earlier
// version of this store cached its last parse keyed by the file's mtime,
// and needed its own invalidate-on-Save logic plus a dedicated race test to
// defend against a cross-process staleness hazard that existed only
// because the cache existed (#1963 review) — removing the cache removes
// that hazard, it does not trade it for a smaller one.
type fileEnrollStore struct {
	path string
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

// Load reads the on-disk record set. A missing file is not an error — no
// code has been minted yet, matching loadTokens' treatment of a missing
// tokens.json.
func (f *fileEnrollStore) Load() ([]onetimecode.Record, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []enrollCodeRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", f.path, err)
	}
	out := make([]onetimecode.Record, len(recs))
	for i, r := range recs {
		out[i] = onetimecode.Record{Key: r.Hash, Workspace: r.Workspace, Label: r.Label, ExpiresAt: r.ExpiresAt}
	}
	return out, nil
}

func (f *fileEnrollStore) Save(records []onetimecode.Record) error {
	recs := make([]enrollCodeRecord, len(records))
	for i, r := range records {
		recs[i] = enrollCodeRecord{Hash: r.Key, Workspace: r.Workspace, Label: r.Label, ExpiresAt: r.ExpiresAt}
	}
	return saveEnrollRecords(f.path, recs)
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
