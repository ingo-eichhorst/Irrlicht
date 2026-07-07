package main

import "testing"

func TestNextPageURL(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{
			name:   "next among multiple rels",
			header: `<https://api.github.com/repos/o/r/stargazers?page=2>; rel="next", <https://api.github.com/repos/o/r/stargazers?page=5>; rel="last"`,
			want:   "https://api.github.com/repos/o/r/stargazers?page=2",
		},
		{
			name:   "no next rel",
			header: `<https://api.github.com/repos/o/r/stargazers?page=1>; rel="prev"`,
			want:   "",
		},
		{
			name:   "empty header",
			header: "",
			want:   "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextPageURL(c.header); got != c.want {
				t.Errorf("nextPageURL(%q) = %q, want %q", c.header, got, c.want)
			}
		})
	}
}
