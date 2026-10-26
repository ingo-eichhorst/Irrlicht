package righome

import (
	"strings"
	"testing"
)

// TestTheOpenCodeDaemonNeverNamesTheServiceSecret is the tripwire #2109's
// record asks for: the opencode 2.x service's service.json carries its
// password, the rig's driver reads it to authenticate, and the daemon must
// not. A comment saying so (pid.go) is a claim; this is the check. It fails
// on any production string literal containing "service.json" in the opencode
// adapter package — the only way Go code opens a file by that name.
func TestTheOpenCodeDaemonNeverNamesTheServiceSecret(t *testing.T) {
	hits, err := OpenCodeServiceSecretLiterals(repoRoot(t))
	if err != nil {
		t.Fatalf("scanning %s: %v — a tripwire that cannot look must say so", OpenCodeAdapterDir, err)
	}
	for _, h := range hits {
		t.Errorf("%s/%s:%d names %q in a string literal. The 2.x service's %s holds its "+
			"password; the daemon finds the service from its argv (pid.go servicePIDFor) and must "+
			"never open that file — only the opencode-v2 rig's driver does, to authenticate.",
			OpenCodeAdapterDir, h.File, h.Line, h.Value, ServiceSecretFile)
	}
}

// TestSecretFileLiteralsNamesEveryShape is the scanner's own corpus: what it
// must find, what it must ignore, and the refusal that keeps "nothing to scan"
// from reading as "nothing found".
func TestSecretFileLiteralsNamesEveryShape(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		want    int
		wantErr string
	}{
		{
			name:  "a plain literal",
			files: map[string]string{"pid.go": "package p\nvar f = \"service.json\"\n"},
			want:  1,
		},
		{
			name:  "a path built with the literal",
			files: map[string]string{"pid.go": "package p\nimport \"path/filepath\"\nvar f = filepath.Join(\"opencode\", \"service.json\")\n"},
			want:  1,
		},
		{
			name:  "a raw-string literal",
			files: map[string]string{"pid.go": "package p\nvar f = `/state/opencode/service.json`\n"},
			want:  1,
		},
		{
			name:  "a comment is not a read",
			files: map[string]string{"pid.go": "package p\n// Never through the service's service.json.\nvar f = 1\n"},
			want:  0,
		},
		{
			name: "a test file may name it",
			files: map[string]string{
				"pid.go":      "package p\nvar f = 1\n",
				"pid_test.go": "package p\nvar g = \"service.json\"\n",
			},
			want: 0,
		},
		{
			name:    "no production source refuses",
			files:   map[string]string{"pid_test.go": "package p\n"},
			wantErr: "no production Go source",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := SecretFileLiterals(tc.files, ServiceSecretFile)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(hits) != tc.want {
				t.Errorf("found %d literal(s) %+v, want %d", len(hits), hits, tc.want)
			}
		})
	}
}
