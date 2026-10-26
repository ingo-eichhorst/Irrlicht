package opencode

import (
	"database/sql"

	"irrlicht/core/pkg/sqlitero"
)

// newestObservedCLIVersion is the hooks gate's passive version source
// (agent.VersionGate.Observed, #2108): the opencode version that created the
// newest session in the store the daemon reads, or "" when the store answers
// nothing.
//
// It is what picks the plugin artifact, so it has to name the CLI the user runs
// NOW, and that is why it orders by time_created rather than time_updated. A
// 2.x migration imports every 1.x session into session_v2 with its old version
// ('1.19.4' in #2104 item 2) and its old timestamps. Whether resuming one of
// those under 2.x moves its time_updated or rewrites its version was not
// measured, and ordering by creation does not depend on the answer: the newest
// session CREATED is one the current CLI created.
//
// Both tables are read because both majors write one: 2.x creates sessions in
// session_v2, 1.x in session, and a store 2.x migrated keeps 1.x's frozen
// (#2104 item 2). session_v2 is asked first — when it has a row, a 2.x has
// opened the store, and 1.x hangs on a store 2.x migrated (#2104 item 3), so
// the 1.x table's newest row is history. Each query is read-only through
// sqlitero; a store that does not exist, a table that does not, or an empty one
// answers "" and the gate falls through to its probe.
//
// Reads opencode's session database, so the gate declares
// ObservedRequires: PermissionKeyDatabase and PermissionService consults this
// only while that permission is granted.
func newestObservedCLIVersion() string {
	db, err := sqlitero.Open(StorePath())
	if err != nil {
		return ""
	}
	defer db.Close()
	for _, query := range []string{newestV2SessionVersionQuery, newestV1SessionVersionQuery} {
		if v := firstString(db, query); v != "" {
			return v
		}
	}
	return ""
}

// newestV2SessionVersionQuery and newestV1SessionVersionQuery read the version
// of the newest session each major's table holds. id breaks a tie in
// time_created so the answer is deterministic.
const (
	newestV2SessionVersionQuery = `SELECT version FROM session_v2 ORDER BY time_created DESC, id DESC LIMIT 1`
	newestV1SessionVersionQuery = `SELECT version FROM session ORDER BY time_created DESC, id DESC LIMIT 1`
)

// firstString returns the single string query selects, or "" on any error —
// no such table, no row, an unopenable store.
func firstString(db *sql.DB, query string) string {
	var s sql.NullString
	if err := db.QueryRow(query).Scan(&s); err != nil {
		return ""
	}
	return s.String
}
