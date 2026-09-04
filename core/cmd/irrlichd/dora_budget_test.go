package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"irrlicht/core/adapters/outbound/git"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/dora"
	"irrlicht/core/domain/session"
)

// TestDoraGitBudgetCoversOneHistoryWalk is what makes services.DoraGitBudget's
// value evidence rather than a number someone liked, and it lives HERE because
// this is the only package that may see both sides: core/architecture_test.go
// forbids application/services from importing an adapter, so the service that
// owns the budget cannot read the ceiling it has to cover.
//
// The relation is the one thing about that value that is derived rather than
// judged. There is no server-side write ceiling behind this handler
// (startup.go sets WriteTimeout to 0 deliberately), so nothing bounds the
// budget from above except what a browser will wait for — but from BELOW it is
// pinned: an aggregate shorter than one history ceiling means a single
// legitimate `git log` on a large repository can never complete inside it, so
// the DORA panel is permanently blank for exactly the repositories #1553 raised
// that ceiling for. Lowering DoraGitBudget under this line would silently undo
// #1553 for this path while every other test stayed green.
func TestDoraGitBudgetCoversOneHistoryWalk(t *testing.T) {
	if services.DoraGitBudget < git.HistoryCeiling {
		t.Errorf("services.DoraGitBudget (%v) is shorter than git.HistoryCeiling (%v): a DORA computation could "+
			"not finish ONE commit-range walk on a repository at #1553's design point, so the panel would be "+
			"permanently unavailable for every repository that ceiling was raised for (#1563)",
			services.DoraGitBudget, git.HistoryCeiling)
	}
}

// TestHandleGetHistory_DoraHonoursTheRequestContext pins the second, free bound
// #1563 buys: the git walks run under the REQUEST's context, so a browser that
// navigates away — or a daemon shutting down — stops them, instead of leaving
// up to DoraGitBudget of `git log` running for a response nobody will read.
//
// It is asserted through the RESPONSE rather than by watching processes,
// because that is what the budget contract already guarantees: a cancelled
// context makes the first git call a non-answer, and #1543's polarity turns
// that into a blank panel rather than a claim about the repository. A handler
// wired to context.Background() computes the whole thing and answers
// Available:true.
func TestHandleGetHistory_DoraHonoursTheRequestContext(t *testing.T) {
	git := fakeDoraGit{
		tags: []dora.TagInfo{{Name: "v1.0", Epoch: 0}, {Name: "v1.1", Epoch: 14 * 86400}},
		commitsByTag: map[string][]dora.CommitInfo{
			"v1.0": {{Hash: "a", AuthorEpoch: 0, Body: "initial"}},
			"v1.1": {{Hash: "b", AuthorEpoch: 14*86400 - 3600, Body: "feat: add widget"}},
		},
	}
	lister := fakeYieldLister{sessions: []*session.SessionState{doraSession("s1", "alpha", "/repo/alpha")}}
	url := "/api/v1/history?chart=dora&project=alpha&start=0&end=" + strconv.Itoa(14*86400+1)

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, url, nil).WithContext(gone)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, lister, nil, git, nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (an unreadable panel is a well-formed answer, not an error), got %d: %s", rec.Code, rec.Body.String())
	}
	var resp historyDoraResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if resp.Available {
		t.Errorf("the client was gone before the first git call and the handler computed DORA anyway (%+v). "+
			"Passing context.Background() instead of r.Context() leaves the walks running for a response nobody "+
			"reads, on a server whose WriteTimeout is deliberately 0 (#1563)", resp)
	}

	// Vacuity guard: the same fixture on a live request MUST produce a panel,
	// or the assertion above holds for a handler that never computes anything.
	live := httptest.NewRecorder()
	handleGetHistory(nil, lister, nil, git, nil)(live, httptest.NewRequest(http.MethodGet, url, nil))
	var ok historyDoraResponse
	if err := json.Unmarshal(live.Body.Bytes(), &ok); err != nil {
		t.Fatalf("decode %q: %v", live.Body.String(), err)
	}
	if !ok.Available {
		t.Errorf("a live request produced %+v, want an available panel — if this fails, the assertion above "+
			"passes for a handler that always answers unavailable", ok)
	}
}
