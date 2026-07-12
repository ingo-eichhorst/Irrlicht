// Command starhistory renders this repo's stargazer history to a static
// SVG, so the README's star-history chart doesn't depend on a live render
// from a third-party service (api.star-history.com) succeeding at
// page-load time. Run from the repo root via `go run ./tools/starhistory`.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
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

// fetchStargazers pages through GET /repos/{repo}/stargazers, requesting
// the starred_at timestamp via the star+json media type.
func fetchStargazers(ctx context.Context, repo, token string) ([]stargazer, error) {
	var all []stargazer
	url := fmt.Sprintf("https://api.github.com/repos/%s/stargazers?per_page=100", repo)
	client := &http.Client{Timeout: 30 * time.Second}
	for url != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github.star+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
		}
		var page []stargazer
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		url = nextPageURL(resp.Header.Get("Link"))
	}
	return all, nil
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextPageURL extracts the rel="next" URL from a GitHub API Link header,
// or "" once there are no more pages.
func nextPageURL(linkHeader string) string {
	m := linkNextRe.FindStringSubmatch(linkHeader)
	if m == nil {
		return ""
	}
	return m[1]
}
