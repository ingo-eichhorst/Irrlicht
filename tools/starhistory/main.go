// Command starhistory renders this repo's stargazer history to a static
// SVG, so the README's star-history chart doesn't depend on a live render
// from a third-party service (api.star-history.com) succeeding at
// page-load time. Run from the repo root via `go run ./tools/starhistory`.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"irrlicht/tools/starhistory/chart"
)

type stargazer struct {
	StarredAt time.Time `json:"starred_at"`
}

func main() {
	repo := flag.String("repo", "", "GitHub repo as owner/name")
	out := flag.String("out", "", "output SVG file path")
	starsJSON := flag.String("stars-json", "", "path to a cached GitHub stargazers JSON response (application/vnd.github.star+json shape) — skips the live API fetch when set, for local testing without a token")
	flag.Parse()

	if *repo == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: starhistory -repo owner/name -out path/to/chart.svg")
		os.Exit(1)
	}

	var stars []stargazer
	var err error
	if *starsJSON != "" {
		stars, err = loadStargazersFile(*starsJSON)
	} else {
		stars, err = fetchStargazers(context.Background(), *repo, os.Getenv("GITHUB_TOKEN"))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch stargazers:", err)
		os.Exit(1)
	}

	times := make([]time.Time, len(stars))
	for i, s := range stars {
		times[i] = s.StarredAt
	}
	series := chart.BuildSeries(times)
	svg := chart.RenderSVG(series, *repo, time.Now())

	if err := os.WriteFile(*out, []byte(svg), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write svg:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d stars)\n", *out, len(stars))
}

func loadStargazersFile(path string) ([]stargazer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var stars []stargazer
	if err := json.Unmarshal(data, &stars); err != nil {
		return nil, err
	}
	return stars, nil
}

// graphqlEndpoint is a var so tests can point the fetch at a stub server.
var graphqlEndpoint = "https://api.github.com/graphql"

const stargazersQuery = `query($owner:String!,$name:String!,$after:String){
  repository(owner:$owner,name:$name){
    stargazers(first:100,after:$after,orderBy:{field:STARRED_AT,direction:ASC}){
      pageInfo{hasNextPage endCursor}
      edges{starredAt}
    }
  }
}`

// stargazerPage is one page of the GraphQL stargazers connection.
type stargazerPage struct {
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
	Edges []struct {
		StarredAt time.Time `json:"starredAt"`
	} `json:"edges"`
}

// stargazerRepository is the `data.repository` object. It is referenced by
// pointer above so a null repository — the shape GraphQL returns when the
// name is wrong or the token can't see it — stays distinguishable from a
// real repository that simply has no stargazers yet.
type stargazerRepository struct {
	Stargazers stargazerPage `json:"stargazers"`
}

type stargazersResponse struct {
	Data struct {
		Repository *stargazerRepository `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// fetchStargazers pages through the GraphQL stargazers connection, which
// carries the starredAt timestamp per edge.
//
// The REST equivalent (GET /repos/{repo}/stargazers with the
// application/vnd.github.star+json media type) is not usable from CI: it
// 403s for the Actions installation token ("Resource not accessible by
// integration", #1012) and, since 2026-07-24, for personal access tokens
// too ("Resource not accessible by personal access token"). GraphQL
// returns the same data for both token types, so this path needs no
// PAT-scoped secret.
func fetchStargazers(ctx context.Context, repo, token string) ([]stargazer, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN is empty: the GraphQL API requires authentication")
	}

	fetcher := stargazerFetcher{
		client: &http.Client{Timeout: 30 * time.Second},
		token:  token,
		owner:  owner,
		name:   name,
	}

	var all []stargazer
	var after any
	for {
		page, err := fetcher.page(ctx, after)
		if err != nil {
			return nil, err
		}
		for _, e := range page.Edges {
			all = append(all, stargazer{StarredAt: e.StarredAt})
		}
		if !page.PageInfo.HasNextPage || page.PageInfo.EndCursor == "" {
			return all, nil
		}
		after = page.PageInfo.EndCursor
	}
}

// stargazerFetcher holds the per-run request context for paging through
// one repo's stargazers connection.
type stargazerFetcher struct {
	client *http.Client
	token  string
	owner  string
	name   string
}

// page fetches the stargazers page starting at the given cursor (nil for
// the first page).
func (f stargazerFetcher) page(ctx context.Context, after any) (stargazerPage, error) {
	body, err := f.post(ctx, after)
	if err != nil {
		return stargazerPage{}, err
	}
	return f.parsePage(body)
}

func (f stargazerFetcher) post(ctx context.Context, after any) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{
		"query":     stargazersQuery,
		"variables": map[string]any{"owner": f.owner, "name": f.name, "after": after},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("POST %s: %s: %s", graphqlEndpoint, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (f stargazerFetcher) parsePage(body []byte) (stargazerPage, error) {
	var parsed stargazersResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return stargazerPage{}, err
	}
	if len(parsed.Errors) > 0 {
		msgs := make([]string, len(parsed.Errors))
		for i, e := range parsed.Errors {
			msgs[i] = e.Message
		}
		return stargazerPage{}, fmt.Errorf("graphql: %s", strings.Join(msgs, "; "))
	}
	if parsed.Data.Repository == nil {
		return stargazerPage{}, fmt.Errorf("graphql: no repository %s/%s in response", f.owner, f.name)
	}
	return parsed.Data.Repository.Stargazers, nil
}

// splitRepo splits an "owner/name" repo reference into its two halves.
func splitRepo(repo string) (owner, name string, err error) {
	// A repo with no "/" cuts to an empty name, so the halves alone tell
	// us whether the reference was well-formed.
	owner, name, _ = strings.Cut(repo, "/")
	if owner == "" || name == "" {
		return "", "", fmt.Errorf("repo %q is not in owner/name form", repo)
	}
	return owner, name, nil
}
