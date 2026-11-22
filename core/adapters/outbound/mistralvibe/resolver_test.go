package mistralvibe

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"irrlicht/core/adapters/outbound/chromecookies"
)

// fixturePassword is the KNOWN TEST password
// core/adapters/outbound/chromecookies/testdata/make_fixture.py encrypted
// Cookies-v24 with. It never came from a real Keychain.
const fixturePassword = "irrlicht-test-safe-storage-password"

const fixtureStore = "../chromecookies/testdata/Cookies-v24"

var fixtureNow = time.Date(2026, 11, 21, 13, 37, 45, 0, time.UTC)

// fakeChrome lays out a Chrome user data dir in a temp directory: Local
// State naming profile, and that profile's Cookies copied from store.
func fakeChrome(t *testing.T, profile, store string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, profile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Local State"), []byte(`{"profile":{"last_used":"`+profile+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if store == "" {
		return root
	}
	b, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Cookies"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// testBrowser is a Browser over root whose key is the fixture key, counting
// every read of it (each one stands for a Keychain read).
func testBrowser(t *testing.T, root string) (Browser, *atomic.Int64) {
	t.Helper()
	k, err := chromecookies.DeriveKey(fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	return Browser{
		UserDataDir: func() (string, error) { return root, nil },
		Key:         func(context.Context) (chromecookies.Key, error) { reads.Add(1); return k, nil },
		Now:         func() time.Time { return fixtureNow },
	}, &reads
}

// TestSession_ResolveSendsOnlyTheSelectedCookies is #2012 §7.1's target, end
// to end through the real Chrome reader against the committed fixture: the
// credential holds exactly csrftoken and the one live ory_session_* cookie,
// none of the six other cookies beside them in the store, and X-CSRFToken
// repeats the csrftoken value. TestHTTPTransport_SendsExactlyTheCredentialHeaders
// (accountquota) is the wire half: those bytes are what reaches the server.
func TestSession_ResolveSendsOnlyTheSelectedCookies(t *testing.T) {
	b, _ := testBrowser(t, fakeChrome(t, "Default", fixtureStore))
	s, err := b.Session()
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	cred, err := s.Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	const want = "csrftoken=csrf-fixture-value; ory_session_coolcurranf83m3srkfl=ory-fixture-session"
	if got := cred.Reveal(); got != want {
		t.Fatalf("Cookie header = %q\nwant exactly    %q — a cookie outside the selection would leave the machine", got, want)
	}
	if v := cred.RevealCompanion(); v != "csrf-fixture-value" || Auth().CompanionHeader != "X-CSRFToken" {
		t.Fatalf("companion = %s: %q, want X-CSRFToken: csrf-fixture-value", Auth().CompanionHeader, v)
	}
}

// TestSession_AccountIsTheProfileNeverACookie is the target of the second
// §7.4 mutation (#2012 testing strategy, item 4: "derive an account
// reference from a cookie value"). Rotating every cookie value must not move
// the account context, it must not carry any cookie value, and computing it
// must not read the Keychain.
func TestSession_AccountIsTheProfileNeverACookie(t *testing.T) {
	root := fakeChrome(t, "Profile 1", "")
	store := filepath.Join(root, "Profile 1", "Cookies")
	writePlainStore(t, store, map[string]string{"csrftoken": "csrf-A", "ory_session_x": "session-A"})
	b, keyReads := testBrowser(t, root)

	s, err := b.Session()
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	first := s.Account()
	writePlainStore(t, store, map[string]string{"csrftoken": "csrf-B", "ory_session_x": "session-B"})
	s2, err := b.Session()
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	second := s2.Account()

	if first != second {
		t.Fatalf("account context changed when only a cookie value changed: %q -> %q", first, second)
	}
	if slices.ContainsFunc([]string{"csrf-A", "session-A", "csrf-B", "session-B"}, func(v string) bool { return strings.Contains(first, v) }) {
		t.Fatalf("account context %q carries a cookie value", first)
	}
	if want := "chrome-profile:" + filepath.Join(root, "Profile 1"); first != want {
		t.Fatalf("Account() = %q, want %q", first, want)
	}
	if n := keyReads.Load(); n != 0 {
		t.Fatalf("computing the account context read the Keychain-backed key %d time(s)", n)
	}
}

// TestSession_TwoProfilesAreTwoAccounts: a different profile is a different
// account context and never shares a reading.
func TestSession_TwoProfilesAreTwoAccounts(t *testing.T) {
	a, _ := testBrowser(t, fakeChrome(t, "Default", fixtureStore))
	b, _ := testBrowser(t, fakeChrome(t, "Profile 2", fixtureStore))
	sa, err := a.Session()
	if err != nil {
		t.Fatal(err)
	}
	sb, err := b.Session()
	if err != nil {
		t.Fatal(err)
	}
	if sa.Account() == sb.Account() {
		t.Fatalf("two profiles share the account context %q", sa.Account())
	}
}

// TestSession_NoChromeNeverReadsTheKeychain: a machine (or a test daemon's
// temporary $HOME) with no Chrome fails before any Keychain read.
func TestSession_NoChromeNeverReadsTheKeychain(t *testing.T) {
	b, keyReads := testBrowser(t, t.TempDir())
	if _, err := b.Session(); !errors.Is(err, chromecookies.ErrProfileMissing) {
		t.Fatalf("Session with no Chrome: err = %v, want ErrProfileMissing", err)
	}
	b, keyReads2 := testBrowser(t, signedOutChrome(t))
	s, err := b.Session()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(t.Context()); !errors.Is(err, chromecookies.ErrCookieAbsent) {
		t.Fatalf("Resolve signed out: err = %v, want ErrCookieAbsent", err)
	}
	if keyReads.Load()+keyReads2.Load() != 0 {
		t.Fatal("the Keychain-backed key was read with no Chrome session to decrypt")
	}
}

// signedOutChrome lays out a Chrome user data dir whose Default profile has
// a csrftoken cookie and no session cookie: a browser signed out of Mistral.
func signedOutChrome(t *testing.T) string {
	t.Helper()
	root := fakeChrome(t, "Default", "")
	writePlainStore(t, filepath.Join(root, "Default", "Cookies"), map[string]string{"csrftoken": "c"})
	return root
}

// writePlainStore writes a Chrome-schema store of session cookies on
// .mistral.ai with plaintext values (the format Chromium also loads).
func writePlainStore(t *testing.T, path string, cookies map[string]string) {
	t.Helper()
	_ = os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE meta(key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR);
		INSERT INTO meta VALUES ('version', '24');
		CREATE TABLE cookies(creation_utc INTEGER NOT NULL, host_key TEXT NOT NULL, name TEXT NOT NULL,
		value TEXT NOT NULL, encrypted_value BLOB NOT NULL, path TEXT NOT NULL, expires_utc INTEGER NOT NULL,
		has_expires INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	i := 0
	for name, value := range cookies {
		i++
		if _, err := db.Exec(`INSERT INTO cookies VALUES (?, '.mistral.ai', ?, ?, X'', '/', 0, 0)`, i, name, value); err != nil {
			t.Fatal(err)
		}
	}
}
