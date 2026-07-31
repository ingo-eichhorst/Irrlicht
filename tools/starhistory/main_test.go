package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSplitRepoSplitsOwnerAndName(t *testing.T) {
	cases := []struct {
		name      string
		repo      string
		wantOwner string
		wantName  string
	}{
		{name: "owner and name", repo: "ingo-eichhorst/Irrlicht", wantOwner: "ingo-eichhorst", wantName: "Irrlicht"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			owner, name, err := splitRepo(c.repo)
			if err != nil {
				t.Fatalf("splitRepo(%q) unexpected err: %v", c.repo, err)
			}
			if owner != c.wantOwner || name != c.wantName {
				t.Errorf("splitRepo(%q) = %q, %q, want %q, %q", c.repo, owner, name, c.wantOwner, c.wantName)
			}
		})
	}
}

func TestSplitRepoRejectsMalformed(t *testing.T) {
	cases := []struct {
		name       string
		repo       string
		wantErrSub string
	}{
		{name: "no slash", repo: "Irrlicht", wantErrSub: "owner/name"},
		{name: "empty owner", repo: "/Irrlicht", wantErrSub: "owner/name"},
		{name: "empty name", repo: "ingo-eichhorst/", wantErrSub: "owner/name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := splitRepo(c.repo)
			if err == nil || !strings.Contains(err.Error(), c.wantErrSub) {
				t.Fatalf("splitRepo(%q) err = %v, want error containing %q", c.repo, err, c.wantErrSub)
			}
		})
	}
}

// stubGraphQL serves the given pages in order, echoing each request's
// `after` cursor back so the test can assert pagination is threaded.
func stubGraphQL(t *testing.T, pages []string, seenAfter *[]any) *httptest.Server {
	t.Helper()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		var req struct {
			Variables struct {
				After any `json:"after"`
			} `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("unmarshal request: %v", err)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want bearer test-token", got)
		}
		*seenAfter = append(*seenAfter, req.Variables.After)
		if calls >= len(pages) {
			t.Errorf("unexpected request %d, only %d pages stubbed", calls+1, len(pages))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, pages[calls])
		calls++
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withEndpoint(t *testing.T, url string) {
	t.Helper()
	prev := graphqlEndpoint
	graphqlEndpoint = url
	t.Cleanup(func() { graphqlEndpoint = prev })
}

func TestFetchStargazersPaginates(t *testing.T) {
	pages := []string{
		`{"data":{"repository":{"stargazers":{"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"},
			"edges":[{"starredAt":"2025-09-07T17:15:20Z"},{"starredAt":"2025-09-08T15:03:41Z"}]}}}}`,
		`{"data":{"repository":{"stargazers":{"pageInfo":{"hasNextPage":false,"endCursor":"cursor-2"},
			"edges":[{"starredAt":"2025-10-13T13:09:07Z"}]}}}}`,
	}
	var seenAfter []any
	srv := stubGraphQL(t, pages, &seenAfter)
	withEndpoint(t, srv.URL)

	stars, err := fetchStargazers(context.Background(), "ingo-eichhorst/Irrlicht", "test-token")
	if err != nil {
		t.Fatalf("fetchStargazers: %v", err)
	}

	want := []time.Time{
		time.Date(2025, 9, 7, 17, 15, 20, 0, time.UTC),
		time.Date(2025, 9, 8, 15, 3, 41, 0, time.UTC),
		time.Date(2025, 10, 13, 13, 9, 7, 0, time.UTC),
	}
	if len(stars) != len(want) {
		t.Fatalf("got %d stargazers, want %d", len(stars), len(want))
	}
	for i, w := range want {
		if !stars[i].StarredAt.Equal(w) {
			t.Errorf("stars[%d].StarredAt = %v, want %v", i, stars[i].StarredAt, w)
		}
	}
	if len(seenAfter) != 2 {
		t.Fatalf("got %d requests, want 2", len(seenAfter))
	}
	if seenAfter[0] != nil {
		t.Errorf("first request after = %v, want null", seenAfter[0])
	}
	if seenAfter[1] != "cursor-1" {
		t.Errorf("second request after = %v, want cursor-1", seenAfter[1])
	}
}

// A truthful hasNextPage with an empty cursor would otherwise loop forever
// re-fetching page one.
func TestFetchStargazersStopsOnEmptyCursor(t *testing.T) {
	pages := []string{
		`{"data":{"repository":{"stargazers":{"pageInfo":{"hasNextPage":true,"endCursor":""},
			"edges":[{"starredAt":"2025-09-07T17:15:20Z"}]}}}}`,
	}
	var seenAfter []any
	srv := stubGraphQL(t, pages, &seenAfter)
	withEndpoint(t, srv.URL)

	stars, err := fetchStargazers(context.Background(), "ingo-eichhorst/Irrlicht", "test-token")
	if err != nil {
		t.Fatalf("fetchStargazers: %v", err)
	}
	if len(stars) != 1 || len(seenAfter) != 1 {
		t.Fatalf("got %d stars over %d requests, want 1 over 1", len(stars), len(seenAfter))
	}
}

func TestFetchStargazersErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		token      string
		repo       string
		wantErrSub string
	}{
		{
			name:       "graphql errors array",
			status:     http.StatusOK,
			body:       `{"errors":[{"message":"Bad credentials"}]}`,
			wantErrSub: "Bad credentials",
		},
		{
			name:       "http error status",
			status:     http.StatusUnauthorized,
			body:       `{"message":"Requires authentication"}`,
			wantErrSub: "401",
		},
		{
			name:       "null repository",
			status:     http.StatusOK,
			body:       `{"data":{"repository":null}}`,
			wantErrSub: "no repository",
		},
		{
			name:       "empty token is rejected before any request",
			token:      "-",
			wantErrSub: "GITHUB_TOKEN is empty",
		},
		{
			name:       "malformed repo is rejected before any request",
			repo:       "Irrlicht",
			wantErrSub: "owner/name",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				io.WriteString(w, c.body)
			}))
			defer srv.Close()
			withEndpoint(t, srv.URL)

			repo := c.repo
			if repo == "" {
				repo = "ingo-eichhorst/Irrlicht"
			}
			token := "test-token"
			if c.token == "-" {
				token = ""
			}
			_, err := fetchStargazers(context.Background(), repo, token)
			if err == nil || !strings.Contains(err.Error(), c.wantErrSub) {
				t.Fatalf("fetchStargazers err = %v, want error containing %q", err, c.wantErrSub)
			}
		})
	}
}
