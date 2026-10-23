package filesystem

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"irrlicht/core/domain/session"
)

func TestAccountHomeStore_MissingFileIsEmpty(t *testing.T) {
	got, err := NewAccountHomeStore(t.TempDir()).Load()
	if err != nil || len(got) != 0 {
		t.Fatalf("Load() = %v, %v; want empty, nil", got, err)
	}
}

func TestAccountHomeStore_RoundTripAndMode(t *testing.T) {
	dir := t.TempDir()
	s := NewAccountHomeStore(dir)
	want := map[string]session.HomeAccount{"/h/.claude.json": {Ref: "A", Since: 42}, "/x/.claude.json": {Since: 7}}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := NewAccountHomeStore(dir).Load()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %v, %v; want %v", got, err, want)
	}
	fi, err := os.Stat(filepath.Join(dir, "account-homes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestAccountHomeStore_CorruptFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "account-homes.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAccountHomeStore(dir).Load(); err == nil {
		t.Fatal("Load() of a corrupt file returned no error")
	}
}
