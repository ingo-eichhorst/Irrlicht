package main

// fileEnrollStore carries no cache (#1963 review: an earlier mtime-keyed
// cache introduced a cross-process staleness hazard that existed only
// because the cache existed — see enroll_store.go's own doc). The property
// that still matters, now that there is nothing to go stale: a Save from
// one store instance is observed by a second, independent store instance
// over the same path.

import (
	"testing"
	"time"

	"irrlicht/core/pkg/onetimecode"
)

func TestFileEnrollStoreSaveIsVisibleToAnotherInstance(t *testing.T) {
	ddir := t.TempDir()
	path := resolveEnrollCodesPath(ddir)
	writer := newFileEnrollStore(path)
	reader := newFileEnrollStore(path)

	rec := onetimecode.Record{Key: "some-hash", Workspace: "acme", ExpiresAt: time.Now().Add(time.Hour)}
	if err := writer.Save([]onetimecode.Record{rec}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	recs, err := reader.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(recs) != 1 || recs[0].Key != "some-hash" {
		t.Fatalf("Load() on a second store instance = %+v, want the first instance's Save", recs)
	}
}
