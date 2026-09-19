package main

// A real cross-process race in fileEnrollStore, distinct from the one
// triage accepted. saveEnrollRecords renames a temp file into place, and
// Save then stats the now-renamed path to cache its mtime. If another
// process's own rename into the same path lands in the window between our
// rename and our stat, our Save caches THAT rename's mtime against OUR OWN
// (different) content. The next Load compares the file's current mtime to
// that cached value, finds them equal, and returns the cache — permanently
// serving our stale content and never re-reading the other process's write,
// not merely until the next external change. authStore does not have this
// form: its write path (issueToken, tokens.go) re-reads from disk on every
// mutation and never consults a cache, so there is nothing for a
// same-mtime collision to poison.
//
// os.Chtimes forces the mtime collision deterministically instead of
// racing two real processes.

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"irrlicht/core/pkg/onetimecode"
)

func TestFileEnrollStoreLoadNoticesExternalWriteAtCachedMtime(t *testing.T) {
	ddir := t.TempDir()
	path := resolveEnrollCodesPath(ddir)
	store := newFileEnrollStore(path)

	// Our own write, through the real Save path.
	ownRec := onetimecode.Record{Key: "own-hash", Workspace: "acme", ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Save([]onetimecode.Record{ownRec}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	savedMtime := fi.ModTime()

	// Simulate another process's rename landing at the exact mtime our own
	// Save just cached: different content, same mtime.
	externalRec := enrollCodeRecord{Hash: "external-hash", Workspace: "other", ExpiresAt: time.Now().Add(time.Hour)}
	data, err := json.MarshalIndent([]enrollCodeRecord{externalRec}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, savedMtime, savedMtime); err != nil {
		t.Fatal(err)
	}

	recs, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(recs) != 1 || recs[0].Key != "external-hash" {
		t.Fatalf("Load() = %+v, want the external write (Key=\"external-hash\") — the store trusted its own post-Save mtime stamp over an external rewrite that landed at the exact same mtime", recs)
	}
}
