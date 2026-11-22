package chromecookies

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"irrlicht/core/pkg/sqlitero"
)

// Selector names the only cookies Read returns. It is also a requirement:
// every entry in Names, and every prefix in NamePrefixes, must match at least
// one unexpired cookie on one of Hosts, or Read fails with ErrCookieAbsent
// before it ever asks for the key.
type Selector struct {
	// Hosts are exact host_key values, e.g. "console.mistral.ai" for a
	// host-only cookie and ".mistral.ai" for a domain cookie.
	Hosts []string
	// Names are exact cookie names.
	Names []string
	// NamePrefixes select every cookie whose name starts with the prefix,
	// compared case-sensitively.
	NamePrefixes []string
}

// Cookie is one selected, decrypted cookie. Its value is unexported and
// String/GoString are redacted, so formatting a Cookie (or a slice of them)
// prints only its name.
type Cookie struct {
	Name    string
	HostKey string
	value   string
}

// Value returns the decrypted cookie value. Its one intended caller builds
// the request credential (mistralvibe.BuildCredential).
func (c Cookie) Value() string { return c.value }

func (c Cookie) String() string   { return c.Name + "=[redacted]" }
func (c Cookie) GoString() string { return c.String() }

// windowsEpochOffset is the number of microseconds between 1601-01-01 and
// 1970-01-01 UTC: Chrome's expires_utc counts from the former.
const windowsEpochOffset = 11644473600 * 1_000_000

// row is one matching cookies row before decryption.
type row struct {
	hostKey, name, plaintext string
	encrypted                []byte
	expiresUTC               int64
	hasExpires               bool
}

// Read returns the cookies in the database at storePath that sel selects and
// that are unexpired at now, decrypted with the key from key.
//
// The order is deliberate and is what TestRead_NeverReadsTheKeychainBeforeItHasTo
// checks: the file must exist, then the database must answer, then every
// required cookie must be present and unexpired — and only then is key
// called. key is the step that reads the Keychain (and can raise a macOS
// dialog), so a missing Chrome, a missing profile, or a signed-out browser
// never reaches it. A daemon booted with a temporary $HOME, as the test
// suite's do, therefore never touches the Keychain at all.
//
// Only rows matching sel are ever selected from SQLite; no other cookie is
// decrypted, returned, or held in memory by this package.
func Read(ctx context.Context, storePath string, sel Selector, now time.Time, key func(context.Context) (Key, error)) ([]Cookie, error) {
	query, args, err := sel.query()
	if err != nil {
		return nil, err
	}
	db, version, err := openStore(ctx, storePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, classifyQueryError("selecting cookies", err)
	}
	defer rows.Close()
	matched, err := unexpiredRows(rows, now)
	if err != nil {
		return nil, err
	}

	if missing := sel.firstMissing(matched); missing != "" {
		return nil, fmt.Errorf("%w: %s", ErrCookieAbsent, missing)
	}

	k, err := key(ctx)
	if err != nil {
		return nil, keychainError(err)
	}
	return decryptAll(k, matched, version)
}

// openStore opens the cookie database at storePath read-only and reads its
// meta version. The caller closes the database.
func openStore(ctx context.Context, storePath string) (*sql.DB, int, error) {
	if _, err := os.Stat(storePath); err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrStoreMissing, err)
	}
	db, err := sqlitero.Open(storePath)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: opening: %w", ErrStoreUnreadable, err)
	}
	var versionText string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'version'`).Scan(&versionText); err != nil {
		_ = db.Close()
		return nil, 0, classifyQueryError("reading meta.version", err)
	}
	version, err := strconv.Atoi(versionText)
	if err != nil {
		_ = db.Close()
		return nil, 0, fmt.Errorf("%w: meta.version %q is not a number", ErrStoreUnreadable, versionText)
	}
	return db, version, nil
}

// unexpiredRows scans every selected row and keeps the ones unexpired at now.
func unexpiredRows(rows *sql.Rows, now time.Time) ([]row, error) {
	var matched []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.hostKey, &r.name, &r.plaintext, &r.encrypted, &r.expiresUTC, &r.hasExpires); err != nil {
			return nil, classifyQueryError("scanning a cookie", err)
		}
		if !r.expired(now) {
			matched = append(matched, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, classifyQueryError("iterating cookies", err)
	}
	return matched, nil
}

// keychainError classifies a failed key read: one already classified as
// ErrKeychain or ErrNotImplemented is returned as is, anything else is
// wrapped as ErrKeychain.
func keychainError(err error) error {
	if errors.Is(err, ErrKeychain) || errors.Is(err, ErrNotImplemented) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrKeychain, err)
}

// decryptAll decrypts every matched row with k, failing on the first value
// that does not decrypt.
func decryptAll(k Key, matched []row, version int) ([]Cookie, error) {
	out := make([]Cookie, 0, len(matched))
	for _, r := range matched {
		v, err := decryptValue(k, r, version)
		if err != nil {
			return nil, fmt.Errorf("cookie %q on %q: %w", r.name, r.hostKey, err)
		}
		out = append(out, Cookie{Name: r.name, HostKey: r.hostKey, value: v})
	}
	return out, nil
}

// expired reports whether r had expired by now. A session cookie
// (has_expires 0) never expires on its own.
func (r row) expired(now time.Time) bool {
	if !r.hasExpires || r.expiresUTC == 0 {
		return false
	}
	return time.UnixMicro(r.expiresUTC - windowsEpochOffset).Before(now)
}

// query builds the one SELECT Read runs. Every host and name condition is a
// bound parameter, never spliced text. Rows come back in the order RFC 6265
// §5.4 step 2 gives for a Cookie header: longer path first, then earlier
// creation time first.
func (sel Selector) query() (string, []any, error) {
	if len(sel.Hosts) == 0 || len(sel.Names)+len(sel.NamePrefixes) == 0 {
		return "", nil, errors.New("chromecookies: a Selector needs at least one host and one name or prefix")
	}
	var args []any
	hosts := make([]string, 0, len(sel.Hosts))
	for _, h := range sel.Hosts {
		hosts = append(hosts, "?")
		args = append(args, h)
	}
	var names []string
	for _, n := range sel.Names {
		names = append(names, "name = ?")
		args = append(args, n)
	}
	for _, p := range sel.NamePrefixes {
		if p == "" {
			return "", nil, errors.New("chromecookies: an empty name prefix would select every cookie")
		}
		names = append(names, "substr(name, 1, ?) = ?")
		args = append(args, utf8.RuneCountInString(p), p)
	}
	q := `SELECT host_key, name, value, encrypted_value, expires_utc, has_expires FROM cookies` +
		` WHERE host_key IN (` + strings.Join(hosts, ", ") + `)` +
		` AND (` + strings.Join(names, " OR ") + `)` +
		` ORDER BY length(path) DESC, creation_utc ASC`
	return q, args, nil
}

// firstMissing names the first Names entry or NamePrefixes prefix that no
// row in matched satisfies, or "" when all are present.
func (sel Selector) firstMissing(matched []row) string {
	if n, ok := firstUnmatched(sel.Names, matched, func(r row, n string) bool { return r.name == n }); ok {
		return n
	}
	if p, ok := firstUnmatched(sel.NamePrefixes, matched, func(r row, p string) bool { return strings.HasPrefix(r.name, p) }); ok {
		return p + "*"
	}
	return ""
}

// firstUnmatched returns the first entry of want that match pairs with no
// row in matched.
func firstUnmatched(want []string, matched []row, match func(row, string) bool) (string, bool) {
	for _, w := range want {
		if !slices.ContainsFunc(matched, func(r row) bool { return match(r, w) }) {
			return w, true
		}
	}
	return "", false
}

// classifyQueryError maps a SQLite failure to ErrStoreLocked when another
// connection's lock caused it, and to ErrStoreUnreadable otherwise. It
// matches the driver's message text, which is all database/sql exposes
// without importing the driver's own error type. The text is the one
// TestRead_LockedStoreIsClassified logs against a store held under
// `PRAGMA locking_mode = EXCLUSIVE`: "database is locked (5) (SQLITE_BUSY)".
func classifyQueryError(what string, err error) error {
	msg := err.Error()
	if strings.Contains(msg, "database is locked") || strings.Contains(msg, "SQLITE_BUSY") {
		return fmt.Errorf("%w: %s: %w", ErrStoreLocked, what, err)
	}
	return fmt.Errorf("%w: %s: %w", ErrStoreUnreadable, what, err)
}
