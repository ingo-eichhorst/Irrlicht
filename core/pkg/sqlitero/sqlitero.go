// Package sqlitero opens an agent's SQLite store read-only.
//
// Three adapters read a store the agent owns — hermes and opencode
// (agent.ProcessOwnedStore) and antigravity (its per-conversation
// conversations/<id>.db) — and all three declare an OBSERVE-kind permission
// whose consent copy promises the user that no row is ever written. That
// promise is not free: getting it wrong is silent, because a store that is
// opened writable reads exactly the same as one that isn't.
//
// Two ways it was got wrong, both verified against modernc.org/sqlite
// v1.55.0 and both fixed here:
//
//   - A BARE-PATH DSN ("/path/db?mode=ro") does not open read-only at all.
//     modernc only parses DSN query parameters for a file: URI; given a bare
//     path it strips the query and opens SQLITE_OPEN_READWRITE|CREATE. So
//     "mode=ro" is silently ignored: against a real opencode store, a
//     bare-path handle accepted a CREATE TABLE, and against a missing path it
//     CREATED the database.
//   - A file: DSN with the path CONCATENATED in re-reads that path as a URI,
//     and the store path is user-controlled ($HERMES_HOME, XDG dirs). See
//     Open for what each structural character does.
//
// The fix is one helper rather than three copies because the failure is
// invisible in review: every spelling looks equally read-only at the call
// site, and only the DSN grammar decides which one is.
package sqlitero

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver; no CGo required
)

// ErrUnsupportedPath rejects a store path containing "?" instead of opening
// it, because both ways of spelling it are worse than being blind:
//
//   - Left raw, "?" starts the URI's query string. The driver still reads the
//     right rows, but on the way there it creates a stray empty database at
//     the TRUNCATED path — a write, from an observe-kind permission.
//   - Escaped as "%3F", modernc v1.55.0 does not decode it back and the open
//     fails with "unable to open database file (14)". Escaping buys nothing
//     over refusing.
//
// Refusing keeps the promise, and the blast radius is one pathological store
// location: "?" is illegal in a Windows path and vanishingly rare elsewhere.
var ErrUnsupportedPath = errors.New(`store path contains "?", which SQLite reads as a URI query — refusing to open it read-only`)

// uriPathEscaper percent-encodes the two structural characters that DO
// round-trip through modernc (verified in TestOpen_URISpecialCharsInPath):
//
//   - "#" starts a fragment, truncating the path and carrying "mode=ro" away
//     with it — the stray-write shape described on ErrUnsupportedPath.
//   - "%" starts a percent-escape, so an unescaped one mis-decodes the path
//     or fails the open outright, and the adapter goes silently blind.
//
// "%" is listed first only for readability: strings.Replacer matches at each
// position in ONE pass, so an emitted "%25" is never rescanned. Spaces need no
// escaping (verified) and are left alone. Not net/url: url.URL.String() leaves
// "?" unescaped in a path, which is exactly the character that must not
// survive.
var uriPathEscaper = strings.NewReplacer("%", "%25", "#", "%23")

// Open returns a read-only handle on the SQLite store at dbPath.
//
// The parameter set is fixed, and deliberately does NOT include the
// "_journal=WAL" that opencode and antigravity carried before this: modernc
// applies it by executing `PRAGMA journal_mode=WAL` on the connection, which
// is a WRITE to the agent's store whenever that store is not already in WAL
// mode. It only ever appeared to work because those handles were writable —
// the same defect this package exists to close. A reader does not choose a
// journal mode; it reads whichever one the store is in. (Pinned by
// TestOpen_RefusesToSetJournalMode.)
//
// _timeout bounds the wait on a writer's lock instead of stalling the
// caller's scan loop.
//
// Returns ErrUnsupportedPath for a path this cannot open safely, so a caller
// that cannot honour read-only reads nothing rather than writing.
func Open(dbPath string) (*sql.DB, error) {
	if strings.ContainsRune(dbPath, '?') {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedPath, dbPath)
	}
	return sql.Open("sqlite", "file:"+uriPathEscaper.Replace(dbPath)+"?mode=ro&_timeout=500")
}
