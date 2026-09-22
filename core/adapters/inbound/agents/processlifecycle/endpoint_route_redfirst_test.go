package processlifecycle

import "testing"

// TestEnvOf_CannotScopeToEndpointKeys is issue #2002's red-first defect
// test. Before the fix, EnvOf(pid int) (map[string]string, error) took no
// key-set parameter, so EVERY caller — regardless of which permission it
// held — was filtered by the one package-level launcherEnvKeys whitelist. A
// caller holding only the endpoint-observation permission (wanting only
// ANTHROPIC_BASE_URL) had no way to say so, and got launcherEnvKeys'
// answer instead: it received a launcher key it had no consent for
// (TERM_PROGRAM) and never received the endpoint key it asked for
// (ANTHROPIC_BASE_URL, which isn't in launcherEnvKeys at all).
//
// Observed failing on origin/main (466019490 base) before this fix existed,
// calling the one-arg osProc.EnvOf(pid) that was main's only signature:
//
//	endpoint_route_redfirst_test.go:28: a caller wanting only the endpoint key
//	set must receive ANTHROPIC_BASE_URL; got map map[TERM_PROGRAM:iTerm.app] (value "")
//	endpoint_route_redfirst_test.go:31: a caller wanting only the endpoint key
//	set must NOT receive TERM_PROGRAM (outside its own permission's key set);
//	got "iTerm.app" in map map[TERM_PROGRAM:iTerm.app]
//	--- FAIL: TestEnvOf_CannotScopeToEndpointKeys (0.05s)
//
// This file now calls the fixed two-arg signature and passes — the
// permanent regression test for the scoping the port gained.
//
// Run against a real child process (not a fake ProcessObserver) so the
// assertion is about the actual OS-backed filter, not a test double that
// could trivially be made to agree with either signature.
func TestEnvOf_CannotScopeToEndpointKeys(t *testing.T) {
	pid := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=iTerm.app",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:8080/v1",
	})

	got, _ := osProc.EnvOf(pid, endpointEnvKeys)

	if v, ok := got["ANTHROPIC_BASE_URL"]; !ok {
		t.Errorf("a caller wanting only the endpoint key set must receive ANTHROPIC_BASE_URL; got map %v (value %q)", got, v)
	}
	if v, ok := got["TERM_PROGRAM"]; ok {
		t.Errorf("a caller wanting only the endpoint key set must NOT receive TERM_PROGRAM (outside its own permission's key set); got %q in map %v", v, got)
	}
}
