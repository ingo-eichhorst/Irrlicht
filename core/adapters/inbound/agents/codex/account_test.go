package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/accountfile"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/ports/outbound"
)

func always() bool { return true }

func authJSON(mode, id string) string {
	return fmt.Sprintf(`{"auth_mode":%q,"OPENAI_API_KEY":"sk-SECRET","tokens":{"id_token":"SECRET","access_token":"SECRET","refresh_token":"SECRET","account_id":%q}}`, mode, id)
}

// codexHomeRig lays down <root>/auth.json and a rollout under
// <root>/sessions/2026/01/02/ and returns the rollout path.
func codexHomeRig(t *testing.T, root, auth, rolloutBody string) string {
	t.Helper()
	if auth != "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		authPath := filepath.Join(root, "auth.json")
		if err := os.WriteFile(authPath, []byte(auth), 0o600); err != nil {
			t.Fatal(err)
		}
		// Login predates the rollout's timestamp, so the binding rule can
		// attribute the session (it began after the account was logged in).
		old := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(authPath, old, old); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, "sessions", "2026", "01", "02")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-x.jsonl")
	if err := os.WriteFile(p, []byte(rolloutBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func codexSess(id, transcript string) *session.SessionState {
	return &session.SessionState{SessionID: id, Adapter: AdapterName, TranscriptPath: transcript}
}

const metaLine = `{"timestamp":"2026-01-02T03:04:05.5Z","type":"session_meta","payload":{"id":"x"}}` + "\n"

// B3 (red-first, written against a stub reader returning !ok): two sessions
// under different CODEX_HOME roots are each attributed to THEIR root's
// account, never to ~/.codex's.
func TestAccountReader_EachCodexHomeRootUsesItsOwnAuthFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(authJSON("chatgpt", "WRONG-home")), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	a := codexHomeRig(t, filepath.Join(tmp, "homeA"), authJSON("chatgpt", "acct-A"), metaLine)
	b := codexHomeRig(t, filepath.Join(tmp, "homeB"), authJSON("chatgpt", "acct-B"), metaLine)

	r := NewAccountReader(always)
	for _, tc := range []struct{ path, want string }{{a, "acct-A"}, {b, "acct-B"}} {
		obs, ok := r.ReadHomeAccount(codexSess("s", tc.path))
		if !ok || obs.Ref != tc.want || obs.Product != "" {
			t.Errorf("%s: obs=%+v ok=%v, want ref %q", tc.path, obs, ok, tc.want)
		}
	}

	// Through the binder: each session's own snapshot is stamped with its
	// own root's account.
	binder := services.NewAccountBinder(map[string]outbound.SessionAccountReader{AdapterName: r}, nil, nopLogger{}, nil)
	for _, tc := range []struct{ path, want string }{{a, "acct-A"}, {b, "acct-B"}} {
		s := codexSess("s-"+tc.want, tc.path)
		s.Metrics = &session.SessionMetrics{RateLimit: &session.RateLimitSnapshot{SampledAt: 1800000000}}
		binder.Stamp(s)
		if got := s.Metrics.RateLimit.ConfirmedAccountRef; got != tc.want {
			t.Errorf("binder stamped %q, want %q (path %s)", got, tc.want, tc.path)
		}
	}
}

// nopLogger satisfies outbound.Logger; the binder calls it only on a store
// failure and these tests pass no store.
type nopLogger struct{ outbound.Logger }

func TestAccountReader_AuthModes(t *testing.T) {
	tmp := t.TempDir()
	r := NewAccountReader(always)
	for _, tc := range []struct {
		name, auth, wantRef string
		wantOK              bool
	}{
		{"chatgpt", authJSON("chatgpt", "acct-1"), "acct-1", true},
		{"apikey", authJSON("apikey", "ignored"), "", true},
		{"no mode", `{"tokens":{"account_id":"acct-1"}}`, "", true},
		{"unparseable first read", `{"auth_mo`, "", false},
		{"missing", "", "", true}, // not-exist is an answer: no account
	} {
		p := codexHomeRig(t, filepath.Join(tmp, strings.ReplaceAll(tc.name, " ", "_")), tc.auth, metaLine)
		obs, ok := r.ReadHomeAccount(codexSess("s", p))
		if ok != tc.wantOK || obs.Ref != tc.wantRef {
			t.Errorf("%s: obs=%+v ok=%v, want ref %q ok %v", tc.name, obs, ok, tc.wantRef, tc.wantOK)
		}
	}
}

func TestAccountReader_TransientFailureKeepsLastGood(t *testing.T) {
	root := t.TempDir()
	p := codexHomeRig(t, root, authJSON("chatgpt", "acct-1"), metaLine)
	r := NewAccountReader(always)
	if obs, _ := r.ReadHomeAccount(codexSess("s", p)); obs.Ref != "acct-1" {
		t.Fatalf("first read: %+v", obs)
	}
	if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(`{"auth_mode":"chat`), 0o600); err != nil {
		t.Fatal(err)
	}
	if obs, ok := r.ReadHomeAccount(codexSess("s", p)); !ok || obs.Ref != "acct-1" {
		t.Errorf("torn write: obs=%+v ok=%v, want last good", obs, ok)
	}
}

func TestAccountReader_OnlyTheSessionsLayoutIsAttributed(t *testing.T) {
	root := t.TempDir()
	codexHomeRig(t, root, authJSON("chatgpt", "acct-1"), metaLine)
	r := NewAccountReader(always)
	for _, p := range []string{
		"",
		filepath.Join(root, "rollout-x.jsonl"),
		filepath.Join(root, "sessions", "rollout-x.jsonl"),
		filepath.Join(root, "sessions", "2026", "01", "rollout-x.jsonl"),
		filepath.Join(root, "other", "2026", "01", "02", "rollout-x.jsonl"),
		filepath.Join(root, "sessions", "2026", "01", "02", "03", "rollout-x.jsonl"),
	} {
		if obs, ok := r.ReadHomeAccount(codexSess("s", p)); ok {
			t.Errorf("path %q attributed: %+v", p, obs)
		}
	}
}

func TestAccountReader_AllowlistKeepsOnlyTheNamedFields(t *testing.T) {
	p := codexHomeRig(t, t.TempDir(), authJSON("chatgpt", "acct-1"), metaLine)
	obs, _ := NewAccountReader(always).ReadHomeAccount(codexSess("s", p))
	if rendered := fmt.Sprintf("%+v %#v", obs, obs); strings.Contains(rendered, "SECRET") {
		t.Errorf("observation leaks a credential: %s", rendered)
	}
}

func TestAccountReader_TranscriptStart(t *testing.T) {
	body := "not json\n" + `{"type":"x"}` + "\n" + metaLine
	p := codexHomeRig(t, t.TempDir(), authJSON("chatgpt", "a"), body)
	if got, want := NewAccountReader(always).TranscriptStart(codexSess("s", p)), int64(1767323045); got != want {
		t.Errorf("TranscriptStart = %d, want %d", got, want)
	}
	if got := NewAccountReader(func() bool { return false }).TranscriptStart(codexSess("s", p)); got != 0 {
		t.Errorf("not granted = %d, want 0", got)
	}
	if got := NewAccountReader(always).TranscriptStart(codexSess("s", p+".missing")); got != 0 {
		t.Errorf("missing = %d, want 0", got)
	}
}

func countingAccountFS(n *atomic.Int64) accountfile.FS {
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

func TestAccountReader_CachesByMtimeAndSize(t *testing.T) {
	p := codexHomeRig(t, t.TempDir(), authJSON("chatgpt", "acct-1"), metaLine)
	var n atomic.Int64
	r := newAccountReader(always, countingAccountFS(&n))
	r.ReadHomeAccount(codexSess("s", p))
	first := n.Load()
	r.ReadHomeAccount(codexSess("s", p))
	if got := n.Load() - first; got != 1 {
		t.Errorf("unchanged file cost %d touches on the second read, want 1 (stat only)", got)
	}
}

// Guard: while the codex observe permission is pending or denied neither
// auth.json nor the rollout is touched. The gate is a live per-call check
// (AccountReader.allowed) keyed on the adapter's observe permission.
func TestAccountReader_GateContract(t *testing.T) {
	p := codexHomeRig(t, t.TempDir(), authJSON("chatgpt", "acct-1"), metaLine)
	observe := Agent().Permissions[0]
	if observe.Kind != permission.KindObserve {
		t.Fatalf("Permissions[0] is %v, want the observe permission", observe.Kind)
	}
	gate := contracttesting.NewConsentGate()
	var touches atomic.Int64
	r := newAccountReader(func() bool { return gate.Granted(AdapterName, observe.Key) }, countingAccountFS(&touches))

	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key:       observe.Key,
		OtherKeys: []string{PermissionKeyHooks},
		SetState:  contracttesting.OnlyKey(observe.Key, func(s permission.State) { gate.SetState(observe.Key, s) }),
		Exercise: func() {
			touches.Store(0)
			r.ReadHomeAccount(codexSess("s", p))
			r.TranscriptStart(codexSess("s", p))
		},
		Observe: func() bool { return touches.Load() > 0 },
	})
}

func TestAccountReader_NilGrantedNeverReads(t *testing.T) {
	p := codexHomeRig(t, t.TempDir(), authJSON("chatgpt", "acct-1"), metaLine)
	var n atomic.Int64
	r := newAccountReader(nil, countingAccountFS(&n))
	r.ReadHomeAccount(codexSess("s", p))
	r.TranscriptStart(codexSess("s", p))
	if n.Load() != 0 {
		t.Errorf("nil granted touched the filesystem %d times", n.Load())
	}
}

func TestObservePermissionNamesTheAuthFileFields(t *testing.T) {
	p := Agent().Permissions[0]
	text := p.Touches + p.Detail
	for _, want := range []string{"auth.json", "auth_mode", "account_id"} {
		if !strings.Contains(text, want) {
			t.Errorf("observe permission text does not name %q", want)
		}
	}
}
