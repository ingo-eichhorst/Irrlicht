package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"irrlicht/core/domain/dora"
	"irrlicht/core/domain/session"
)

// fakeDoraGit is a minimal historyGitReader — GetGitRoot resolves any
// non-empty dir to itself (as if every CWD is already a repo root), and the
// other three methods return whatever the test pre-loads, keyed by dir.
type fakeDoraGit struct {
	tags         []dora.TagInfo
	commitsByTag map[string][]dora.CommitInfo
}

func (f fakeDoraGit) GetGitRoot(dir string) string { return dir }
func (f fakeDoraGit) ListReleaseTags(dir string) []dora.TagInfo {
	if dir == "" {
		return nil
	}
	return f.tags
}
func (f fakeDoraGit) CommitsInRange(dir, fromRef, toRef string) []dora.CommitInfo {
	return f.commitsByTag[toRef]
}
func (f fakeDoraGit) TagContaining(dir, hash string) string { return "" }

func doraSession(id, project, cwd string) *session.SessionState {
	return &session.SessionState{SessionID: id, State: session.StateReady, ProjectName: project, CWD: cwd}
}

func TestHandleGetHistory_DoraEndpoint(t *testing.T) {
	git := fakeDoraGit{
		tags: []dora.TagInfo{
			{Name: "v1.0", Epoch: 0},
			{Name: "v1.1", Epoch: 14 * 86400},
		},
		commitsByTag: map[string][]dora.CommitInfo{
			"v1.0": {{Hash: "a", AuthorEpoch: 0, Body: "initial"}},
			"v1.1": {{Hash: "b", AuthorEpoch: 14*86400 - 3600, Body: "feat: add widget"}},
		},
	}
	lister := fakeYieldLister{sessions: []*session.SessionState{doraSession("s1", "alpha", "/repo/alpha")}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?chart=dora&project=alpha&start=0&end="+strconv.Itoa(14*86400+1), nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, lister, nil, git)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp historyDoraResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if !resp.Available {
		t.Fatalf("expected Available=true, got %+v", resp)
	}
	if resp.Project != "alpha" {
		t.Errorf("project = %q, want alpha", resp.Project)
	}
	// Both tags fall in [0, 14*86400+1], each contributing one commit.
	if !resp.LeadTime.Available || resp.LeadTime.SampleSize != 2 {
		t.Errorf("LeadTime = %+v, want available with SampleSize=2", resp.LeadTime)
	}
}

func TestHandleGetHistory_DoraEndpoint_MissingProject(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?chart=dora", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, fakeYieldLister{}, nil, fakeDoraGit{})(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetHistory_DoraEndpoint_MultiProjectRejected(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?chart=dora&project=a,b", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, fakeYieldLister{}, nil, fakeDoraGit{})(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetHistory_DoraEndpoint_ProjectNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?chart=dora&project=nope", nil)
	rec := httptest.NewRecorder()
	handleGetHistory(nil, fakeYieldLister{}, nil, fakeDoraGit{})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 (well-formed request, nothing to compute), got %d: %s", rec.Code, rec.Body.String())
	}
	var resp historyDoraResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if resp.Available {
		t.Fatalf("expected Available=false, got %+v", resp)
	}
}
