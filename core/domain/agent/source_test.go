package agent

import "testing"

func TestFilesUnderRootRootDirFor(t *testing.T) {
	tests := []struct {
		name string
		src  FilesUnderRoot
		goos string
		want string
	}{
		{
			name: "no override falls back to Dir",
			src:  FilesUnderRoot{Dir: ".claude/projects"},
			goos: "linux",
			want: ".claude/projects",
		},
		{
			name: "override applies for matching OS",
			src: FilesUnderRoot{
				Dir:     ".claude/projects",
				DirByOS: map[string]string{"windows": `AppData\Roaming\claude\projects`},
			},
			goos: "windows",
			want: `AppData\Roaming\claude\projects`,
		},
		{
			name: "override ignored for non-matching OS",
			src: FilesUnderRoot{
				Dir:     ".claude/projects",
				DirByOS: map[string]string{"windows": `AppData\Roaming\claude\projects`},
			},
			goos: "darwin",
			want: ".claude/projects",
		},
		{
			name: "empty override value falls back to Dir",
			src: FilesUnderRoot{
				Dir:     ".claude/projects",
				DirByOS: map[string]string{"windows": ""},
			},
			goos: "windows",
			want: ".claude/projects",
		},
		{
			name: "DirFunc supplies the root when set",
			src:  FilesUnderRoot{DirFunc: func() string { return "/srv/logs" }},
			goos: "linux",
			want: "/srv/logs",
		},
		{
			name: "DirFunc outranks a static Dir",
			src: FilesUnderRoot{
				Dir:     ".vibe/logs/session",
				DirFunc: func() string { return "/srv/logs" },
			},
			goos: "linux",
			want: "/srv/logs",
		},
		{
			// DirFunc is "Dir, computed lazily", and DirByOS overrides Dir —
			// so a per-OS override stays authoritative over the resolver.
			name: "DirByOS outranks DirFunc",
			src: FilesUnderRoot{
				DirFunc: func() string { return "/srv/logs" },
				DirByOS: map[string]string{"windows": `AppData\Roaming\vibe`},
			},
			goos: "windows",
			want: `AppData\Roaming\vibe`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.src.RootDirFor(tc.goos); got != tc.want {
				t.Errorf("RootDirFor(%q) = %q, want %q", tc.goos, got, tc.want)
			}
		})
	}
}
