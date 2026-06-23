package services

import "testing"

func TestRedactorString(t *testing.T) {
	r := NewRedactor("/Users/ingo")
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"home path to tilde", "/Users/ingo/projects/foo", "~/projects/foo"},
		{"home only", "/Users/ingo", "~"},
		{"non-home path untouched", "/opt/irrlicht/bin", "/opt/irrlicht/bin"},
		{"openai key", "key=sk-abcdef0123456789ABCDEF tail", "key=[REDACTED] tail"},
		{"anthropic key", "ANTHROPIC_API_KEY=sk-ant-api03-AAaa0011BBbb2233CCcc", "ANTHROPIC_API_KEY=[REDACTED]"},
		{"github pat", "token ghp_0123456789abcdefABCDEF0123456789abcd done", "token [REDACTED] done"},
		{"github oauth", "gho_0123456789abcdefABCDEF0123456789abcd", "[REDACTED]"},
		{"bearer keeps scheme", "Authorization: Bearer eyJhbG.payload.sig", "Authorization: Bearer [REDACTED]"},
		{"bearer lowercase", "authorization: bearer abc.def.ghi", "authorization: bearer [REDACTED]"},
		{"bare sk- not masked", "do sk- this", "do sk- this"},
		{"home and token together", "/Users/ingo ran with sk-abcdef0123456789ABCDEF", "~ ran with [REDACTED]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.String(tt.in); got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRedactorEmptyHomeDisablesPathRewrite(t *testing.T) {
	for _, home := range []string{"", "/"} {
		r := NewRedactor(home)
		// Root path must not be rewritten, but tokens still are.
		if got := r.String("/Users/ingo sk-abcdef0123456789ABCDEF"); got != "/Users/ingo [REDACTED]" {
			t.Errorf("home=%q: got %q", home, got)
		}
	}
}

func TestRedactorArgv(t *testing.T) {
	r := NewRedactor("/Users/ingo")
	if got := r.Argv(nil); got != nil {
		t.Errorf("Argv(nil) = %v, want nil", got)
	}
	in := []string{"claude", "--cwd", "/Users/ingo/p", "--token", "ghp_0123456789abcdefABCDEF0123456789abcd"}
	want := []string{"claude", "--cwd", "~/p", "--token", "[REDACTED]"}
	got := r.Argv(in)
	if len(got) != len(want) {
		t.Fatalf("Argv len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Input must not be mutated in place.
	if in[2] != "/Users/ingo/p" {
		t.Errorf("Argv mutated its input: %v", in)
	}
}
