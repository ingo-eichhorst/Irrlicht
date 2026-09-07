package desktopdriver

import (
	"strings"
	"testing"
)

// The real file, measured on Claude Desktop 1.46388.4, 2026-09-07: a LIST of
// blocklists, each carrying a source URL, its entries and a refresh timestamp.
const measuredBlocklist = `[{"entries":[],` +
	`"lastUpdated":"2026-09-07T17:48:40.980Z",` +
	`"url":"https://claude.ai/api/desktop/extensions-blocklist"}]`

// RED-FIRST: with extensions-blocklist.json guarded by digest, this run failed
// with `app-wide Desktop configuration changed: bytes, type, target, or mode
// differ at ".../extensions-blocklist.json"` — after driving its whole recipe.
// Only lastUpdated had moved, and the app moved it.
func TestBlocklistRefreshIsNotAChangeTheRunMade(t *testing.T) {
	refreshed := strings.Replace(measuredBlocklist,
		"2026-09-07T17:48:40.980Z", "2026-09-07T18:12:03.114Z", 1)
	if refreshed == measuredBlocklist {
		t.Fatal("the fixture did not change, so this test compared a file with itself")
	}
	if err := verifyBlocklistEntries([]byte(measuredBlocklist), []byte(refreshed)); err != nil {
		t.Fatalf("verifyBlocklistEntries() error = %v; a refresh is not a change the run made", err)
	}
}

func TestBlocklistLosesAnEntry(t *testing.T) {
	before := `[{"url":"u","entries":[{"id":"a","name":"Bad","version":"1.0"},` +
		`{"id":"b","name":"Worse","version":"2.0"}],"lastUpdated":"t"}]`
	after := `[{"url":"u","entries":[{"id":"a","name":"Bad","version":"1.0"}],"lastUpdated":"t2"}]`

	err := verifyBlocklistEntries([]byte(before), []byte(after))
	if err == nil {
		t.Fatal("verifyBlocklistEntries() = nil; the run dropped a denied extension")
	}
	if !strings.Contains(err.Error(), "Worse") {
		t.Fatalf("error = %v; want it to name the entry that was removed", err)
	}
}

// A denylist that grew is the app protecting the operator. Refusing it would
// make the driver argue with a security update.
func TestBlocklistMayGrow(t *testing.T) {
	before := `[{"url":"u","entries":[],"lastUpdated":"t"}]`
	after := `[{"url":"u","entries":[{"id":"a","name":"Bad","version":"1.0"}],"lastUpdated":"t2"}]`
	if err := verifyBlocklistEntries([]byte(before), []byte(after)); err != nil {
		t.Fatalf("verifyBlocklistEntries() error = %v; a longer denylist is not a loss", err)
	}
}

// A file it cannot parse is the LAST place to drop the check.
func TestBlocklistThatCannotBeReadIsAFailure(t *testing.T) {
	for _, testCase := range []struct{ name, before, after string }{
		{"the current file is unreadable", measuredBlocklist, `{`},
		{"the baseline is unreadable", `{`, measuredBlocklist},
		{"the shape changed to an object", measuredBlocklist, `{"entries":[]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := verifyBlocklistEntries([]byte(testCase.before), []byte(testCase.after)); err == nil {
				t.Fatal("verifyBlocklistEntries() = nil; an unreadable blocklist must fail loudly")
			}
		})
	}
}

// The guard must not be defeated by moving an entry to a different source list.
func TestBlocklistEntryMovedToAnotherSourceIsStillALoss(t *testing.T) {
	before := `[{"url":"official","entries":[{"id":"a","name":"Bad","version":"1.0"}],"lastUpdated":"t"}]`
	after := `[{"url":"somewhere-else","entries":[{"id":"a","name":"Bad","version":"1.0"}],"lastUpdated":"t"}]`
	if err := verifyBlocklistEntries([]byte(before), []byte(after)); err == nil {
		t.Fatal("verifyBlocklistEntries() = nil; the entry left the list that carried it")
	}
}
