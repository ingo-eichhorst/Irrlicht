package claudecode

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

type fakeRateLimitTarget struct {
	calls []rateLimitCall
}

type rateLimitCall struct {
	path string
	snap *session.RateLimitSnapshot
}

func (f *fakeRateLimitTarget) IngestRateLimit(path string, snap *session.RateLimitSnapshot) {
	f.calls = append(f.calls, rateLimitCall{path: path, snap: snap})
}

type silentLogger struct{}

func (silentLogger) LogInfo(string, string, string)                     {}
func (silentLogger) LogError(string, string, string)                    {}
func (silentLogger) LogProcessingTime(string, string, int64, int, string) {}
func (silentLogger) Close() error                                       { return nil }

func TestStatuslineHandler_IngestsRateLimits(t *testing.T) {
	target := &fakeRateLimitTarget{}
	h := NewStatuslineHandler(target, silentLogger{})

	body := `{
		"session_id": "abc",
		"transcript_path": "/tmp/sessions/abc.jsonl",
		"rate_limits": {
			"five_hour": {"used_percentage": 47, "resets_at": 1778761800},
			"seven_day": {"used_percentage": 14.0, "resets_at": 1779188400}
		}
	}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/hooks/claudecode/statusline", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if len(target.calls) != 1 {
		t.Fatalf("expected one IngestRateLimit call, got %d", len(target.calls))
	}
	call := target.calls[0]
	if call.path != "/tmp/sessions/abc.jsonl" {
		t.Errorf("wrong path: %s", call.path)
	}
	if len(call.snap.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(call.snap.Windows))
	}
	if call.snap.Windows[0].WindowMinutes != 300 || call.snap.Windows[0].UsedPercent != 47 {
		t.Errorf("five_hour mapping wrong: %+v", call.snap.Windows[0])
	}
	if call.snap.Windows[1].WindowMinutes != 10080 {
		t.Errorf("seven_day mapping wrong: %+v", call.snap.Windows[1])
	}
}

func TestStatuslineHandler_NoRateLimitsBlockIsOk(t *testing.T) {
	target := &fakeRateLimitTarget{}
	h := NewStatuslineHandler(target, silentLogger{})

	body := `{"session_id":"abc","transcript_path":"/tmp/abc.jsonl"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for API-key path (no rate_limits), got %d", rr.Code)
	}
	if len(target.calls) != 0 {
		t.Fatalf("expected no IngestRateLimit calls for absent block, got %d", len(target.calls))
	}
}

func TestStatuslineHandler_RejectsMissingTranscriptPath(t *testing.T) {
	h := NewStatuslineHandler(&fakeRateLimitTarget{}, silentLogger{})
	body := `{"session_id":"abc"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestStatuslineHandler_RejectsNonPost(t *testing.T) {
	h := NewStatuslineHandler(&fakeRateLimitTarget{}, silentLogger{})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestStatuslineHandler_SampledAtUsesClock(t *testing.T) {
	pinned := time.Unix(1700000000, 0).UTC()
	prev := statuslineNow
	statuslineNow = func() time.Time { return pinned }
	t.Cleanup(func() { statuslineNow = prev })

	target := &fakeRateLimitTarget{}
	h := NewStatuslineHandler(target, silentLogger{})
	body := `{"transcript_path":"/tmp/x.jsonl","rate_limits":{"five_hour":{"used_percentage":1,"resets_at":2}}}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	if target.calls[0].snap.SampledAt != pinned.Unix() {
		t.Errorf("SampledAt = %d, want %d", target.calls[0].snap.SampledAt, pinned.Unix())
	}
}

func TestChainStatuslineCommand_BareInstall(t *testing.T) {
	got := chainStatuslineCommand("")
	if got != installedStatuslineCommand {
		t.Errorf("expected canonical command on empty input, got %q", got)
	}
}

func TestChainStatuslineCommand_IdempotentOnCanonical(t *testing.T) {
	got := chainStatuslineCommand(installedStatuslineCommand)
	if got != installedStatuslineCommand {
		t.Errorf("expected idempotency, got %q", got)
	}
}

func TestChainStatuslineCommand_WrapsThirdPartyCommand(t *testing.T) {
	user := "/usr/local/bin/my-statusline --foo"
	got := chainStatuslineCommand(user)
	if !strings.Contains(got, user) {
		t.Errorf("expected wrap to preserve user command, got %q", got)
	}
	if !strings.Contains(got, statuslineSentinel) {
		t.Errorf("expected wrap to include our sentinel, got %q", got)
	}
	if !strings.HasPrefix(got, `bash -c 'tee >(`) {
		t.Errorf("expected wrap to start with `bash -c 'tee >(`, got %q", got)
	}
}

func TestChainStatuslineCommand_RewritesStaleManagedForm(t *testing.T) {
	stale := "curl --silent " + statuslineSentinel + " /old"
	got := chainStatuslineCommand(stale)
	if got != installedStatuslineCommand {
		t.Errorf("expected stale managed command to be rewritten to canonical, got %q", got)
	}
}

func TestUnchainStatuslineCommand_RoundTripsUserCommand(t *testing.T) {
	user := "/usr/local/bin/my-statusline --foo"
	wrapped := chainStatuslineCommand(user)
	if got := unchainStatuslineCommand(wrapped); got != user {
		t.Errorf("round-trip mismatch: got %q want %q", got, user)
	}
}

func TestUnchainStatuslineCommand_StandaloneReturnsEmpty(t *testing.T) {
	if got := unchainStatuslineCommand(installedStatuslineCommand); got != "" {
		t.Errorf("expected empty for standalone install, got %q", got)
	}
}

func TestChainStatuslineCommand_WrapUsesBashEnvelope(t *testing.T) {
	user := "/usr/local/bin/my-statusline"
	got := chainStatuslineCommand(user)
	if !strings.HasPrefix(got, `bash -c 'tee >(`) {
		t.Errorf("expected bash -c envelope, got %q", got)
	}
	// v3: curl sits in the process sub; user command is last (ends with user+quote).
	if !strings.HasSuffix(got, user+`'`) {
		t.Errorf("expected user command at end of pipeline, got %q", got)
	}
	if !strings.Contains(got, statuslineSentinel) {
		t.Errorf("expected sentinel in process sub, got %q", got)
	}
}

// TestChainStatuslineCommand_MigratesV1WrapToV2 covers the existing-install
// path: users who picked up the first (broken) statusline wrap end up with a
// `tee >(…) | curl …` command in settings.json that fails under POSIX sh.
// On the next daemon start, we must unwrap their command and re-chain it in
// the bash-envelope form — not overwrite it with the standalone install,
// which would drop their original statusline script.
// TestChainStatuslineCommand_RoundTripsSingleQuotedCommand pins the
// escape contract. The wrap embeds the user command inside
// `bash -c '…'`, so any single quote in the user command has to be
// escaped (POSIX `'\''` idiom) for the wrapped form to parse, AND
// reversed when we unchain it back. Easy to break either side.
func TestChainStatuslineCommand_RoundTripsSingleQuotedCommand(t *testing.T) {
	originals := []string{
		`echo 'hello'`,
		`bash -c 'set -e; echo ok'`,
		`/usr/local/bin/x --flag 'a b' --other "ok"`,
		`echo "no single quotes here"`,
		`printf '%s\n' '<hi>'`,
	}
	for _, original := range originals {
		t.Run(original, func(t *testing.T) {
			wrapped := chainStatuslineCommand(original)
			if !strings.HasPrefix(wrapped, `bash -c 'tee >(`) {
				t.Fatalf("expected v2 wrap, got %q", wrapped)
			}
			unwrapped := unchainStatuslineCommand(wrapped)
			if unwrapped != original {
				t.Errorf("round-trip mismatch:\n  in:  %q\n  out: %q\n  via: %q",
					original, unwrapped, wrapped)
			}
		})
	}
}

// TestUnchainStatuslineCommand_ToleratesExtraWhitespace covers the
// regex-based boundary detection in the unchain path. A hand-edited
// wrap with extra spaces around the pipe should still round-trip
// through unchain → chain.
func TestUnchainStatuslineCommand_ToleratesExtraWhitespace(t *testing.T) {
	hand := `bash -c 'tee >(/usr/local/bin/x)   |  curl -fsS --max-time 1 -X POST --data-binary @- ` +
		`http://localhost:7837/api/v1/hooks/claudecode/statusline >/dev/null 2>&1 || true'`
	if got := unchainStatuslineCommand(hand); got != `/usr/local/bin/x` {
		t.Errorf("expected user command despite extra whitespace, got %q", got)
	}
}

func TestChainStatuslineCommand_MigratesOldWrapsToV3(t *testing.T) {
	userCmd := "bash ~/.claude/interplay-statusline.sh"
	// v1 (no bash envelope)
	v1 := "tee >(" + userCmd + ") | curl -fsS --max-time 1 -X POST --data-binary @- " +
		"http://localhost:7837/api/v1/hooks/claudecode/statusline >/dev/null 2>&1 || true"
	// v2 (user command in process sub)
	v2 := `bash -c 'tee >(` + userCmd + `) | curl -fsS --max-time 1 -X POST --data-binary @- ` +
		`http://localhost:7837/api/v1/hooks/claudecode/statusline >/dev/null 2>&1 || true'`
	for _, old := range []string{v1, v2} {
		got := chainStatuslineCommand(old)
		if !strings.HasPrefix(got, v3WrapPrefix) {
			t.Errorf("expected v3 form after migration, got %q", got)
		}
		if !strings.Contains(got, userCmd) {
			t.Errorf("expected user command to survive migration, got %q", got)
		}
	}
}
