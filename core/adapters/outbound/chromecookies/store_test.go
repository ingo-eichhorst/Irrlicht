package chromecookies

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// fixturePassword is the KNOWN TEST password testdata/make_fixture.py
// encrypted testdata/Cookies-v24 and Cookies-v23 with. It is not, and was
// never derived from, any real Keychain item.
const fixturePassword = "irrlicht-test-safe-storage-password"

// mistralSelector mirrors mistralvibe.CookieSelector. It is restated here
// rather than imported so this package's tests stay free of the provider.
var mistralSelector = Selector{
	Hosts:        []string{"console.mistral.ai", ".mistral.ai"},
	Names:        []string{"csrftoken"},
	NamePrefixes: []string{"ory_session_"},
}

var fixtureNow = time.Date(2026, 11, 21, 13, 37, 45, 0, time.UTC)

func fixtureKey(t *testing.T) func(context.Context) (Key, error) {
	t.Helper()
	k, err := DeriveKey(fixturePassword)
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	return func(context.Context) (Key, error) { return k, nil }
}

// countingKey wraps key and counts its calls: the Keychain-last tests assert
// it stays at zero.
func countingKey(key func(context.Context) (Key, error)) (func(context.Context) (Key, error), *atomic.Int64) {
	var n atomic.Int64
	return func(ctx context.Context) (Key, error) {
		n.Add(1)
		return key(ctx)
	}, &n
}

func summarize(cookies []Cookie) []string {
	out := make([]string, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, c.HostKey+" "+c.Name+"="+c.Value())
	}
	return out
}

// TestRead_SelectsOnlyTheSelectedCookies is #2012 §7.1's lock: against the
// committed fixture (encrypted by openssl, not by this package), Read returns
// exactly the two selected cookies and none of the six traps beside them —
// same host other name, selected name other host, another site, expired,
// case variant, and a name that would match if "_" were a wildcard.
func TestRead_SelectsOnlyTheSelectedCookies(t *testing.T) {
	got, err := Read(t.Context(), "testdata/Cookies-v24", mistralSelector, fixtureNow, fixtureKey(t))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{
		"console.mistral.ai csrftoken=csrf-fixture-value",
		".mistral.ai ory_session_coolcurranf83m3srkfl=ory-fixture-session",
	}
	if strings.Join(summarize(got), "\n") != strings.Join(want, "\n") {
		t.Fatalf("Read returned\n  %s\nwant exactly\n  %s", strings.Join(summarize(got), "\n  "), strings.Join(want, "\n  "))
	}
	for _, c := range got {
		if strings.Contains(c.Value(), "must-not-leave") {
			t.Errorf("Read returned an unselected cookie: %s on %s", c.Name, c.HostKey)
		}
	}
}

// TestRead_DecryptsTheV23Fixture: before meta version 24 there is no host
// hash, and the same values decrypt without one.
func TestRead_DecryptsTheV23Fixture(t *testing.T) {
	got, err := Read(t.Context(), "testdata/Cookies-v23", mistralSelector, fixtureNow, fixtureKey(t))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s := summarize(got); len(s) != 2 || s[0] != "console.mistral.ai csrftoken=csrf-fixture-value" {
		t.Fatalf("Read(v23) = %q", s)
	}
}

// TestRead_RefusesAWrongKey: a key that is not the one the store was
// encrypted with is a classified failure, never garbage returned as a value.
func TestRead_RefusesAWrongKey(t *testing.T) {
	wrong, err := DeriveKey("not-the-test-password")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Read(t.Context(), "testdata/Cookies-v24", mistralSelector, fixtureNow,
		func(context.Context) (Key, error) { return wrong, nil })
	if !errors.Is(err, ErrUnknownEncryption) {
		t.Fatalf("Read with a wrong key: err = %v, want ErrUnknownEncryption", err)
	}
}

// TestRead_RefusesAMissingHostHash: a v23 store relabelled as v24 decrypts
// with valid padding but carries no SHA-256(host_key) prefix. Read must
// refuse it rather than strip 32 bytes of the value.
func TestRead_RefusesAMissingHostHash(t *testing.T) {
	path := copyFixture(t, "testdata/Cookies-v23")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE meta SET value = '24' WHERE key = 'version'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	_, err = Read(t.Context(), path, mistralSelector, fixtureNow, fixtureKey(t))
	if !errors.Is(err, ErrUnknownEncryption) || !strings.Contains(err.Error(), "host hash") {
		t.Fatalf("Read(v23 data as v24): err = %v, want ErrUnknownEncryption naming the host hash", err)
	}
}

// TestRead_NeverReadsTheKeychainBeforeItHasTo pins the order Read's doc
// comment states. Each case is a state the daemon meets on a real machine
// (no Chrome, a signed-out browser, a lapsed session) or in the test suite
// (a temporary $HOME), and in none of them may the Keychain be touched.
func TestRead_NeverReadsTheKeychainBeforeItHasTo(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		path    string
		wantErr error
	}{
		{"no cookie database", filepath.Join(dir, "absent", "Cookies"), ErrStoreMissing},
		{"signed out: no selected cookie", writeStore(t, 24, []plainCookie{{".google.com", "SID", "x", 0}}), ErrCookieAbsent},
		{"csrftoken but no session", writeStore(t, 24, []plainCookie{{"console.mistral.ai", "csrftoken", "c", 0}}), ErrCookieAbsent},
		{"session expired", writeStore(t, 24, []plainCookie{
			{"console.mistral.ai", "csrftoken", "c", 0},
			{".mistral.ai", "ory_session_a", "s", time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro()},
		}), ErrCookieAbsent},
		{"not a cookie database", writeFile(t, "not sqlite at all"), ErrStoreUnreadable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, calls := countingKey(fixtureKey(t))
			_, err := Read(t.Context(), tc.path, mistralSelector, fixtureNow, key)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("the Keychain-backed key was read %d time(s) before every required cookie was known present", n)
			}
		})
	}

	// Vacuity: the same counter does see a read once the cookies are there.
	key, calls := countingKey(fixtureKey(t))
	if _, err := Read(t.Context(), "testdata/Cookies-v24", mistralSelector, fixtureNow, key); err != nil {
		t.Fatalf("Read(fixture): %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("vacuity: the key was read %d times on the happy path, want 1", calls.Load())
	}
}

// TestRead_KeychainFailureIsClassified: a key source that fails is reported
// as ErrKeychain, so the caller can tell a denied dialog from a missing
// cookie.
func TestRead_KeychainFailureIsClassified(t *testing.T) {
	_, err := Read(t.Context(), "testdata/Cookies-v24", mistralSelector, fixtureNow,
		func(context.Context) (Key, error) { return Key{}, errors.New("user denied") })
	if !errors.Is(err, ErrKeychain) {
		t.Fatalf("err = %v, want ErrKeychain", err)
	}
}

// TestRead_LockedStoreIsClassified: a store another connection holds under
// an exclusive lock is ErrStoreLocked, distinct from an unreadable one.
func TestRead_LockedStoreIsClassified(t *testing.T) {
	path := copyFixture(t, "testdata/Cookies-v24")
	holder, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	holder.SetMaxOpenConns(1)
	if _, err := holder.Exec(`PRAGMA locking_mode = EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(`BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = holder.Exec(`ROLLBACK`) }()

	key, calls := countingKey(fixtureKey(t))
	_, err = Read(t.Context(), path, mistralSelector, fixtureNow, key)
	t.Logf("locked store error: %v", err)
	if !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("err = %v, want ErrStoreLocked", err)
	}
	if calls.Load() != 0 {
		t.Fatal("the key was read for a store that could not be queried")
	}
}

func TestSelector_RefusesToSelectEverything(t *testing.T) {
	for _, sel := range []Selector{
		{},
		{Hosts: []string{".mistral.ai"}},
		{Names: []string{"csrftoken"}},
		{Hosts: []string{".mistral.ai"}, NamePrefixes: []string{""}},
	} {
		if _, err := Read(t.Context(), "testdata/Cookies-v24", sel, fixtureNow, fixtureKey(t)); err == nil {
			t.Errorf("Read with selector %+v succeeded, want a refusal", sel)
		}
	}
}

func TestCookieAndKey_FormatRedacted(t *testing.T) {
	got, err := Read(t.Context(), "testdata/Cookies-v24", mistralSelector, fixtureNow, fixtureKey(t))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := fixtureKey(t)(t.Context())
	for _, s := range []string{fmt.Sprintf("%v %+v %#v %s", got, got, got, got), fmt.Sprintf("%v %+v %#v", k, k, k)} {
		if strings.Contains(s, "csrf-fixture-value") || strings.Contains(s, "ory-fixture-session") {
			t.Errorf("formatting leaked a cookie value: %s", s)
		}
	}
	if s := fmt.Sprintf("%#v", k); !strings.Contains(s, "redacted") {
		t.Errorf("Key formatted as %q, want a redaction marker", s)
	}
}

func TestDecryptValue_PlaintextRows(t *testing.T) {
	if v, err := decryptValue(Key{}, ".mistral.ai", "plain", nil, 24); err != nil || v != "plain" {
		t.Errorf("plaintext row: %q, %v", v, err)
	}
	if _, err := decryptValue(Key{}, ".mistral.ai", "plain", []byte("v10xxxxxxxxxxxxxxxx"), 24); !errors.Is(err, ErrUnknownEncryption) {
		t.Errorf("both columns set: err = %v, want ErrUnknownEncryption", err)
	}
	if _, err := decryptValue(Key{b: make([]byte, 16)}, ".mistral.ai", "", []byte("v11xxxxxxxxxxxxxxxx"), 24); !errors.Is(err, ErrUnknownEncryption) {
		t.Errorf("v11 prefix: err = %v, want ErrUnknownEncryption", err)
	}
}

func TestLastUsedProfile(t *testing.T) {
	mk := func(t *testing.T, localState string, dirs ...string) string {
		t.Helper()
		root := t.TempDir()
		for _, d := range dirs {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if localState != "" {
			if err := os.WriteFile(filepath.Join(root, "Local State"), []byte(localState), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	cases := []struct {
		name, localState string
		dirs             []string
		want             string
		wantErr          error
	}{
		{"named profile", `{"profile":{"last_used":"Profile 1"}}`, []string{"Default", "Profile 1"}, "Profile 1", nil},
		{"empty falls back to Default", `{"profile":{}}`, []string{"Default"}, "Default", nil},
		{"system profile falls back to Default", `{"profile":{"last_used":"System Profile"}}`, []string{"Default"}, "Default", nil},
		{"no Local State", "", []string{"Default"}, "", ErrProfileMissing},
		{"profile dir absent", `{"profile":{"last_used":"Profile 9"}}`, []string{"Default"}, "", ErrProfileMissing},
		{"path traversal refused", `{"profile":{"last_used":"../outside"}}`, []string{"Default", "../outside"}, "", ErrProfileMissing},
		{"not JSON", `{`, []string{"Default"}, "", ErrProfileMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := mk(t, tc.localState, tc.dirs...)
			p, err := LastUsedProfile(root)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && (p.Name != tc.want || p.Dir != filepath.Join(root, tc.want) || p.CookiesPath() != filepath.Join(root, tc.want, "Cookies")) {
				t.Fatalf("profile = %+v, want %q under %s", p, tc.want, root)
			}
		})
	}
}

func TestKeyCache_OneReadPerResetAndFailureIsKept(t *testing.T) {
	var reads atomic.Int64
	fail := atomic.Bool{}
	fail.Store(true)
	c := NewKeyCache(func(context.Context) (Key, error) {
		reads.Add(1)
		if fail.Load() {
			return Key{}, ErrKeychain
		}
		return Key{b: []byte("0123456789abcdef")}, nil
	})
	for range 3 {
		if _, err := c.Key(t.Context()); !errors.Is(err, ErrKeychain) {
			t.Fatalf("err = %v, want the cached ErrKeychain", err)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("a failed Keychain read was retried: %d reads before a Reset, want 1", reads.Load())
	}
	fail.Store(false)
	c.Reset()
	if k, err := c.Key(t.Context()); err != nil || k.IsZero() {
		t.Fatalf("after Reset: %v, %v", k, err)
	}
	if reads.Load() != 2 {
		t.Fatalf("reads = %d after a Reset, want 2", reads.Load())
	}
}

func TestKeyCache_ConcurrentCallersShareOneRead(t *testing.T) {
	var reads atomic.Int64
	release := make(chan struct{})
	c := NewKeyCache(func(context.Context) (Key, error) {
		reads.Add(1)
		<-release
		return Key{b: []byte("0123456789abcdef")}, nil
	})
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Key(context.Background()) }()
	}
	deadline := time.Now().Add(5 * time.Second)
	for reads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if reads.Load() != 1 {
		t.Fatalf("five concurrent callers made %d Keychain reads, want 1 (each read can raise a dialog)", reads.Load())
	}
}

// --- fixture helpers ---

type plainCookie struct {
	host, name, value string
	expiresUnixMicro  int64 // 0 = session cookie
}

// writeStore writes a Chrome-schema store holding plaintext rows (an empty
// encrypted_value), the format Chromium also loads. It exercises selection
// and expiry without depending on encryption.
func writeStore(t *testing.T, version int, cookies []plainCookie) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Cookies")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta VALUES ('version', ?)`, fmt.Sprint(version)); err != nil {
		t.Fatal(err)
	}
	for i, c := range cookies {
		var expires, hasExpires int64
		if c.expiresUnixMicro != 0 {
			expires, hasExpires = c.expiresUnixMicro+windowsEpochOffset, 1
		}
		if _, err := db.Exec(`INSERT INTO cookies VALUES (?,?,'',?,?,X'','/',?,1,1,0,?,1,1,0,2,443,0,0,0)`,
			int64(i+1), c.host, c.name, c.value, expires, hasExpires); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Cookies")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func copyFixture(t *testing.T, src string) string {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "Cookies")
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dst
}

const testSchema = `
CREATE TABLE meta(key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR);
CREATE TABLE cookies(creation_utc INTEGER NOT NULL,host_key TEXT NOT NULL,top_frame_site_key TEXT NOT NULL,
name TEXT NOT NULL,value TEXT NOT NULL,encrypted_value BLOB NOT NULL,path TEXT NOT NULL,expires_utc INTEGER NOT NULL,
is_secure INTEGER NOT NULL,is_httponly INTEGER NOT NULL,last_access_utc INTEGER NOT NULL,has_expires INTEGER NOT NULL,
is_persistent INTEGER NOT NULL,priority INTEGER NOT NULL,samesite INTEGER NOT NULL,source_scheme INTEGER NOT NULL,
source_port INTEGER NOT NULL,last_update_utc INTEGER NOT NULL,source_type INTEGER NOT NULL,
has_cross_site_ancestor INTEGER NOT NULL);`
