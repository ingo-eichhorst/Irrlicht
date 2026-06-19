package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Bearer-token auth for the relay. Tokens are minted by the `token issue` CLI,
// printed in plaintext exactly once, and stored only as a SHA-256 hash so the
// tokens file is useless to read at rest (wiki §7). A running `serve` loads the
// hashed set and re-reads it when the file changes, so `token revoke` takes
// effect on the next frame from the revoked peer (closed with CloseRevoked).

// tokensFilename is the basename of the relay's hashed token store, written
// under the relay data dir (honors IRRLICHT_HOME via the caller).
const tokensFilename = "tokens.json"

// TokenRecord is one issued token. Hash is the hex SHA-256 of the plaintext;
// the plaintext itself is never persisted. Label and Created are operator
// metadata for `token list`. Workspace is the tenant scope a connection
// presenting this token is bound to; "" is the default workspace (single-
// tenant, the behavior of every token issued before this field existed).
type TokenRecord struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Hash      string `json:"hash"`
	Created   int64  `json:"created"`
	Workspace string `json:"workspace,omitempty"`
}

// hashToken returns the hex SHA-256 of a plaintext token — the only form stored
// at rest and the key used for constant-time lookup.
func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// loadTokens reads the hashed token store. A missing file is not an error (an
// auth-enabled relay with no tokens yet simply rejects everyone).
func loadTokens(path string) ([]TokenRecord, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []TokenRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return recs, nil
}

// saveTokens writes the hashed token store atomically-ish at mode 0600 (it
// holds credential hashes), creating the data dir if needed.
func saveTokens(path string, recs []TokenRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// issueToken mints a new 256-bit token bound to workspace, appends its hash to
// the store, and returns the id plus the plaintext (shown to the operator once
// and never recoverable thereafter). An empty workspace is the default tenant.
func issueToken(path, label, workspace string) (id, plaintext string, err error) {
	recs, err := loadTokens(path)
	if err != nil {
		return "", "", err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	plaintext = base64.RawURLEncoding.EncodeToString(raw[:])

	var idb [6]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return "", "", err
	}
	id = hex.EncodeToString(idb[:])

	recs = append(recs, TokenRecord{ID: id, Label: label, Hash: hashToken(plaintext), Created: time.Now().Unix(), Workspace: workspace})
	if err := saveTokens(path, recs); err != nil {
		return "", "", err
	}
	return id, plaintext, nil
}

// revokeToken removes the record with the given id. Returns false (no error)
// when no such id exists so the CLI can report it cleanly.
func revokeToken(path, id string) (bool, error) {
	recs, err := loadTokens(path)
	if err != nil {
		return false, err
	}
	out := make([]TokenRecord, 0, len(recs))
	found := false
	for _, r := range recs {
		if r.ID == id {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return false, nil
	}
	return true, saveTokens(path, out)
}

// tokenIdentity is what a valid token resolves to: its id (for revoke
// detection) and the workspace the connection is scoped to.
type tokenIdentity struct {
	id        string
	workspace string
}

// authStore is the relay's live view of valid tokens while serving. It is
// reloaded from disk whenever the tokens file's mtime changes so `token revoke`
// (a separate process editing the file) propagates without a restart.
type authStore struct {
	path string

	mu     sync.RWMutex
	hashes map[string]tokenIdentity // token hash → identity
	byID   map[string]struct{}
	mtime  time.Time
}

// newAuthStore builds a store bound to path and loads it once. A load error is
// returned so `serve` refuses to start with an unreadable tokens file rather
// than silently accepting no one.
func newAuthStore(path string) (*authStore, error) {
	a := &authStore{path: path, hashes: map[string]tokenIdentity{}, byID: map[string]struct{}{}}
	if err := a.reload(); err != nil {
		return nil, err
	}
	return a, nil
}

// reload re-reads the tokens file and swaps in the new hashed set.
func (a *authStore) reload() error {
	recs, err := loadTokens(a.path)
	if err != nil {
		return err
	}
	hashes := make(map[string]tokenIdentity, len(recs))
	byID := make(map[string]struct{}, len(recs))
	for _, r := range recs {
		hashes[r.Hash] = tokenIdentity{id: r.ID, workspace: r.Workspace}
		byID[r.ID] = struct{}{}
	}
	var mtime time.Time
	if fi, err := os.Stat(a.path); err == nil {
		mtime = fi.ModTime()
	}
	a.mu.Lock()
	a.hashes = hashes
	a.byID = byID
	a.mtime = mtime
	a.mu.Unlock()
	return nil
}

// reloadIfChanged reloads only when the tokens file's mtime advanced, so the
// poll loop is cheap on an idle file.
func (a *authStore) reloadIfChanged() {
	fi, err := os.Stat(a.path)
	if err != nil {
		return // file vanished/unreadable: keep the last good set
	}
	a.mu.RLock()
	unchanged := fi.ModTime().Equal(a.mtime)
	a.mu.RUnlock()
	if unchanged {
		return
	}
	_ = a.reload()
}

// watch polls the tokens file for changes until ctx-equivalent stop is closed.
func (a *authStore) watch(stop <-chan struct{}, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			a.reloadIfChanged()
		}
	}
}

// validate reports the token id and workspace for a plaintext token, ok=false
// if the token is empty or unknown. The workspace is server-derived from the
// stored record, never client-supplied — the connection's tenant scope cannot
// be spoofed. The store is keyed by the SHA-256 hash of the token, so a direct
// map lookup of the candidate's hash is O(1) and leaks no secret-dependent
// timing (the hashing already happened; the lookup key is a fixed-width digest
// of an unguessable 256-bit token).
func (a *authStore) validate(plaintext string) (id, workspace string, ok bool) {
	if plaintext == "" {
		return "", "", false
	}
	want := hashToken(plaintext)
	a.mu.RLock()
	defer a.mu.RUnlock()
	ident, ok := a.hashes[want]
	return ident.id, ident.workspace, ok
}

// valid reports whether a token id is still present, used by connection read
// loops to detect a mid-session revoke.
func (a *authStore) valid(id string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.byID[id]
	return ok
}

// sortedRecords returns the on-disk records sorted by creation time for stable
// `token list` output.
func sortedRecords(path string) ([]TokenRecord, error) {
	recs, err := loadTokens(path)
	if err != nil {
		return nil, err
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Created < recs[j].Created })
	return recs, nil
}
