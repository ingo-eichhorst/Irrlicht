package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"irrlicht/core/adapters/inbound/agents/accountfile"
	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
	"irrlicht/core/internal/contracttesting"
)

const identityJSON = `{
  "numStartups": 3,
  "oauthAccount": {
    "accountUuid": "11111111-aaaa",
    "emailAddress": "someone@example.com",
    "organizationUuid": "22222222-bbbb",
    "displayName": "Some One",
    "organizationName": "Secret Org",
    "billingType": "stripe_subscription",
    "seatTier": "premium",
    "organizationType": "claude_team",
    "accessToken": "sk-ant-oat-SECRET"
  },
  "primaryApiKey": "sk-ant-api-SECRET"
}`

// countingFS wraps the real filesystem and counts every touch, the Observe
// side of the consent contract.
func countingFS(n *atomic.Int64) accountfile.FS {
	real := accountfile.OSFS()
	return accountfile.FS{
		Stat: func(p string) (int64, int64, error) { n.Add(1); return real.Stat(p) },
		Read: func(p string) ([]byte, error) { n.Add(1); return real.Read(p) },
		ReadHead: func(p string, k int) ([]byte, error) {
			n.Add(1)
			return real.ReadHead(p, k)
		},
	}
}

func always() bool { return true }

// rig lays down a default-root home under a temp HOME and returns the
// identity file path and a transcript path under the watched root.
func rig(t *testing.T, identity string) (identityPath, transcript string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(configDirEnvVar, "")
	identityPath = filepath.Join(home, ".claude.json")
	if identity != "" {
		if err := os.WriteFile(identityPath, []byte(identity), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(home, ".claude", "projects", "-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript = filepath.Join(dir, "sess.jsonl")
	return identityPath, transcript
}

func sess(transcript string) *session.SessionState {
	return &session.SessionState{SessionID: "s", Adapter: AdapterName, TranscriptPath: transcript}
}

func TestAccountReader_AllowlistKeepsOnlyTheNamedFields(t *testing.T) {
	idPath, tr := rig(t, identityJSON)
	r := NewAccountReader(always)
	obs, ok := r.ReadHomeAccount(sess(tr))
	if !ok {
		t.Fatal("ReadHomeAccount not ok")
	}
	if obs.Ref != "11111111-aaaa" || obs.Product != "claude_team" || obs.Home != idPath || obs.ModTime == 0 {
		t.Fatalf("obs = %+v", obs)
	}
	rendered := fmt.Sprintf("%+v %#v", obs, obs)
	for _, leak := range []string{"someone@example.com", "Some One", "Secret Org", "SECRET", "22222222-bbbb", "stripe", "premium"} {
		if strings.Contains(rendered, leak) {
			t.Errorf("observation leaks %q: %s", leak, rendered)
		}
	}
}

func TestAccountReader_ConfigDirRootUsesItsOwnIdentityFile(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv(configDirEnvVar, cfg)
	if err := os.WriteFile(filepath.Join(cfg, ".claude.json"), []byte(identityJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	// A DIFFERENT account in ~/.claude.json must not be consulted.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"WRONG"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := filepath.Join(cfg, "projects", "-p", "s.jsonl")
	obs, ok := NewAccountReader(always).ReadHomeAccount(sess(tr))
	if !ok || obs.Ref != "11111111-aaaa" || obs.Home != filepath.Join(cfg, ".claude.json") {
		t.Fatalf("obs = %+v ok=%v", obs, ok)
	}
}

func TestAccountReader_OutsideTheWatchedRootIsNotAttributed(t *testing.T) {
	_, _ = rig(t, identityJSON)
	for _, p := range []string{"", "/elsewhere/projects/x/s.jsonl", filepath.Join(os.Getenv("HOME"), ".claude", "x.jsonl"), filepath.Join(os.Getenv("HOME"), ".claude", "projects")} {
		if obs, ok := NewAccountReader(always).ReadHomeAccount(sess(p)); ok {
			t.Errorf("path %q attributed: %+v", p, obs)
		}
	}
}

// expectRead asserts one ReadHomeAccount outcome: ok and, when ok, the ref.
func expectRead(t *testing.T, what string, r *AccountReader, tr string, wantOK bool, wantRef string) {
	t.Helper()
	obs, ok := r.ReadHomeAccount(sess(tr))
	if ok != wantOK || (wantOK && obs.Ref != wantRef) {
		t.Errorf("%s: obs=%+v ok=%v, want ok=%v ref %q", what, obs, ok, wantOK, wantRef)
	}
}

// writeIdentity overwrites the identity file at path.
func writeIdentity(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAccountReader_NoAccountAndTransientFailure(t *testing.T) {
	idPath, tr := rig(t, `{"numStartups":1}`)
	r := NewAccountReader(always)
	expectRead(t, "no account, want ok with empty ref", r, tr, true, "")
	// Valid account, cached; then a torn write must return the last good.
	writeIdentity(t, idPath, identityJSON)
	expectRead(t, "after rewrite", r, tr, true, "11111111-aaaa")
	writeIdentity(t, idPath, `{"oauthAcc`)
	expectRead(t, "torn write, want the last good observation", r, tr, true, "11111111-aaaa")
	// A reader that never saw a good file fails closed.
	_, tr2 := rig(t, `{"oauthAcc`)
	expectRead(t, "unparseable first read", NewAccountReader(always), tr2, false, "")
	// A missing identity file is an answer (no account), not a transient failure.
	_, tr3 := rig(t, "")
	expectRead(t, "missing identity file, want ok with empty ref", NewAccountReader(always), tr3, true, "")
}

func TestAccountReader_CachesByMtimeAndSize(t *testing.T) {
	_, tr := rig(t, identityJSON)
	var n atomic.Int64
	r := newAccountReader(always, countingFS(&n))
	r.ReadHomeAccount(sess(tr))
	first := n.Load() // stat + read
	r.ReadHomeAccount(sess(tr))
	if got := n.Load() - first; got != 1 {
		t.Errorf("unchanged file cost %d touches on the second read, want 1 (stat only)", got)
	}
}

func TestAccountReader_TranscriptStart(t *testing.T) {
	_, tr := rig(t, identityJSON)
	body := `{"type":"file-history-snapshot","snapshot":{"timestamp":"2020-01-01T00:00:00Z"}}` + "\n" +
		`not json` + "\n" +
		`{"type":"user","timestamp":"2026-03-04T05:06:07.123Z"}` + "\n" +
		`{"type":"assistant","timestamp":"2026-03-04T05:07:00Z"}` + "\n"
	if err := os.WriteFile(tr, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := NewAccountReader(always).TranscriptStart(sess(tr)), int64(1772600767); got != want {
		t.Errorf("TranscriptStart = %d, want %d", got, want)
	}
	if got := NewAccountReader(always).TranscriptStart(sess(tr + ".missing")); got != 0 {
		t.Errorf("missing transcript = %d, want 0", got)
	}
	if got := NewAccountReader(func() bool { return false }).TranscriptStart(sess(tr)); got != 0 {
		t.Errorf("not granted = %d, want 0", got)
	}
}

func TestAccountReader_NilGrantedNeverReads(t *testing.T) {
	_, tr := rig(t, identityJSON)
	var n atomic.Int64
	r := newAccountReader(nil, countingFS(&n))
	r.ReadHomeAccount(sess(tr))
	r.TranscriptStart(sess(tr))
	if n.Load() != 0 {
		t.Errorf("nil granted touched the filesystem %d times", n.Load())
	}
}

func TestAccountPermissionDetailNamesEveryReadField(t *testing.T) {
	perms := AccountPermissionDeclaration().Permissions
	if len(perms) != 1 || perms[0].Apply != nil || perms[0].Remove != nil || perms[0].Kind != permission.KindObserve {
		t.Fatalf("want exactly one observe permission with nil closures, got %+v", perms)
	}
	text := perms[0].Detail + perms[0].Touches
	for _, field := range []string{"accountUuid", "organizationType", ".claude.json", "CLAUDE_CONFIG_DIR", "timestamp"} {
		if !strings.Contains(text, field) {
			t.Errorf("consent text does not name %q", field)
		}
	}
}

// TestAccountPermission_GateContract: while the permission is pending or
// denied neither the identity file nor the transcript is touched. The gate is
// a live per-call check (AccountReader.allowed), so the closures are nil and
// SetState only moves the ConsentGate.
func TestAccountPermission_GateContract(t *testing.T) {
	_, tr := rig(t, identityJSON)
	if err := os.WriteFile(tr, []byte(`{"timestamp":"2026-03-04T05:06:07Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	perm := AccountPermissionDeclaration().Permissions[0]
	gate := contracttesting.NewConsentGate()
	var touches atomic.Int64
	r := newAccountReader(func() bool { return gate.Granted(AccountName, perm.Key) }, countingFS(&touches))

	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: perm.Key,
		// One permission is declared, so the key held open beside it is
		// foreign and the key-isolation arm repeats the revoked arm; wired
		// anyway because the contract admits no opt-out.
		OtherKeys: []string{PermissionKeyTranscripts},
		SetState:  contracttesting.OnlyKey(perm.Key, func(s permission.State) { gate.SetState(perm.Key, s) }),
		Exercise: func() {
			touches.Store(0)
			r.ReadHomeAccount(sess(tr))
			r.TranscriptStart(sess(tr))
		},
		Observe: func() bool { return touches.Load() > 0 },
	})
}
