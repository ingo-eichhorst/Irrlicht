// backchannel_store.go persists the single default-OFF master toggle that
// gates the whole backchannel capability — input injection and event→action
// rules (issue #724). A missing file means disabled, which is the fresh and
// upgrade state: control stays off until the user opts in.
package filesystem

import "path/filepath"

// BackchannelStore persists the backchannel master-toggle to
// <dir>/backchannel.json. Storage mechanics live in boolToggle.
type BackchannelStore struct{ *boolToggle }

// NewBackchannelStore returns a store rooted at dir (the daemon data dir),
// loading the persisted value (default false on a missing/unreadable file).
func NewBackchannelStore(dir string) *BackchannelStore {
	return &BackchannelStore{newBoolToggle(filepath.Join(dir, "backchannel.json"))}
}
