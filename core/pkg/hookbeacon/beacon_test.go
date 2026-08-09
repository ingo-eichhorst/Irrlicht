package hookbeacon

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"irrlicht/core/pkg/daemonaddr"
)

// resetCounts zeroes the package counters between tests. Test-only on purpose:
// a counter an operator can reset is a counter that can hide the thing it was
// added to reveal (the same rule hookjson's unknown-event counters keep).
func resetCounts() {
	for i := range counts {
		counts[i].Store(0)
	}
}

// closedPort returns an address nothing is listening on, by binding one and
// releasing it. Every test that must NOT reach a daemon points the beacon here
// rather than leaving resolution to the default port — a developer machine
// usually has a real irrlichd on :7837, and a unit test must never post to it.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("closing the reserved port: %v", err)
	}
	return addr
}

// blockingReader never yields until the test ends. It stands in for an agent CLI
// that opens the hook's stdin pipe and then neither writes nor closes it.
type blockingReader struct{ release <-chan struct{} }

func (b blockingReader) Read([]byte) (int, error) {
	<-b.release
	return 0, io.EOF
}

func newBlockingReader(t *testing.T) blockingReader {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return blockingReader{release: release}
}

// serverAt starts a receiver and points the beacon at it via IRRLICHT_BIND_ADDR.
func serverAt(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv(daemonaddr.EnvBindAddr, strings.TrimPrefix(srv.URL, "http://"))
	return srv
}

const validPayload = `{"hook_event_name":"BeforeTool","transcript_path":"/tmp/x.jsonl"}`

// TestPostAlwaysExitsZero is the acceptance criterion of #1373, one row per
// failure mode.
//
// The property under test is not "the happy path works" — it is that a hook
// command irrlicht installs can never fail the user's tool call. gemini-cli's
// BeforeTool/BeforeAgent hooks turn a non-zero exit into decision "deny" and the
// scheduler then kills the tool with POLICY_VIOLATION; Copilot's preToolUse
// denies on hook error. A `curl` against a down daemon exits 7, which is why the
// naive command is worse than no monitoring at all. Every row below is a way the
// beacon can fail, and every row must still exit 0.
//
// The last row is the vacuity guard: a Post that returned 0 by doing nothing at
// all would satisfy every other row, so one row has to actually deliver.
func TestPostAlwaysExitsZero(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) Options
		want  Reason
		// anyOf is for the one row whose outcome legitimately depends on
		// whether a daemon happens to be running locally.
		anyOf []Reason
	}{
		{
			name: "daemon down: connection refused",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader(validPayload)}
			},
			want: ReasonDaemonUnreachable,
		},
		{
			name: "daemon down: address comes from the published addr file",
			setup: func(t *testing.T) Options {
				home := t.TempDir()
				t.Setenv("IRRLICHT_HOME", home)
				t.Setenv(daemonaddr.EnvBindAddr, "")
				writeAddrFile(t, home, closedPort(t))
				return Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader(validPayload)}
			},
			want: ReasonDaemonUnreachable,
		},
		{
			name: "addr file is unreadable garbage",
			setup: func(t *testing.T) Options {
				home := t.TempDir()
				t.Setenv("IRRLICHT_HOME", home)
				t.Setenv(daemonaddr.EnvBindAddr, "")
				writeAddrFile(t, home, "not-an-address")
				return Options{Args: []string{"irrlicht-no-such-receiver"}, Stdin: strings.NewReader(validPayload)}
			},
			// A garbage addr file falls back to the default port, where a
			// developer machine may well have a real daemon. Either outcome is
			// exit 0; the route is one no daemon serves, so the worst case is a
			// 404 it already answers for any unknown path.
			anyOf: []Reason{ReasonDaemonUnreachable, ReasonDaemonRejected},
		},
		{
			name: "receiver wedged past the deadline",
			setup: func(t *testing.T) Options {
				serverAt(t, func(w http.ResponseWriter, r *http.Request) {
					time.Sleep(5 * time.Second)
				})
				return Options{
					Args: []string{"gemini-cli"}, Stdin: strings.NewReader(validPayload),
					Timeout: 150 * time.Millisecond,
				}
			},
			want: ReasonDeadlineExceeded,
		},
		{
			name: "stdin is opened and never closed",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{
					Args: []string{"gemini-cli"}, Stdin: newBlockingReader(t),
					Timeout: 150 * time.Millisecond,
				}
			},
			want: ReasonDeadlineExceeded,
		},
		{
			name: "stdin errors mid-read",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{
					Args:  []string{"gemini-cli"},
					Stdin: iotest.ErrReader(errors.New("read |0: file already closed")),
				}
			},
			want: ReasonUnreadableStdin,
		},
		{
			name: "stdin returns bytes and then errors: never post a truncated body",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{
					Args:  []string{"gemini-cli"},
					Stdin: iotest.TimeoutReader(strings.NewReader(validPayload + validPayload)),
				}
			},
			want: ReasonUnreadableStdin,
		},
		{
			name: "no stdin attached at all",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{Args: []string{"gemini-cli"}, Stdin: nil}
			},
			want: ReasonEmptyPayload,
		},
		{
			name: "stdin closes with nothing on it",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader("")}
			},
			want: ReasonEmptyPayload,
		},
		{
			name: "stdin carries only whitespace",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader("  \n\t ")}
			},
			want: ReasonEmptyPayload,
		},
		{
			name: "payload over the size cap",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{
					Args:  []string{"gemini-cli"},
					Stdin: strings.NewReader(strings.Repeat("x", maxPayloadBytes+1)),
				}
			},
			want: ReasonPayloadTooLarge,
		},
		{
			name: "malformed stdin: not JSON at all",
			setup: func(t *testing.T) Options {
				serverAt(t, func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
				})
				return Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader("}{not json")}
			},
			// The beacon does not parse the payload — the daemon's 400 is the
			// verdict, and it is still exit 0 here.
			want: ReasonDaemonRejected,
		},
		{
			name: "daemon answers 500",
			setup: func(t *testing.T) Options {
				serverAt(t, func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "boom", http.StatusInternalServerError)
				})
				return Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader(validPayload)}
			},
			want: ReasonDaemonRejected,
		},
		{
			name: "daemon has no route for this adapter",
			setup: func(t *testing.T) Options {
				serverAt(t, func(w http.ResponseWriter, r *http.Request) {
					http.NotFound(w, r)
				})
				return Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader(validPayload)}
			},
			want: ReasonDaemonRejected,
		},
		{
			name: "daemon hangs up mid-response",
			setup: func(t *testing.T) Options {
				serverAt(t, func(w http.ResponseWriter, r *http.Request) {
					conn, _, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Errorf("hijack: %v", err)
						return
					}
					_ = conn.Close()
				})
				return Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader(validPayload)}
			},
			want: ReasonDaemonUnreachable,
		},
		{
			name: "no adapter argument",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{Args: nil, Stdin: strings.NewReader(validPayload)}
			},
			want: ReasonMissingAdapter,
		},
		{
			name: "only the guard token, no adapter",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{Args: []string{LegacyGuardToken}, Stdin: strings.NewReader(validPayload)}
			},
			want: ReasonMissingAdapter,
		},
		{
			name: "adapter argument is a path traversal",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{Args: []string{"../../../debug/bundle"}, Stdin: strings.NewReader(validPayload)}
			},
			want: ReasonInvalidAdapter,
		},
		{
			name: "adapter argument is empty",
			setup: func(t *testing.T) Options {
				t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
				return Options{Args: []string{""}, Stdin: strings.NewReader(validPayload)}
			},
			want: ReasonInvalidAdapter,
		},
		{
			// Vacuity guard: without this row, a Post that always refused would
			// pass every other row above.
			name: "delivered: the daemon is up and answers 200",
			setup: func(t *testing.T) Options {
				serverAt(t, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				})
				return Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader(validPayload)}
			},
			want: ReasonNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetCounts()
			opts := tt.setup(t)
			var stderr bytes.Buffer
			opts.Stderr = &stderr

			start := time.Now()
			code := Post(opts)
			elapsed := time.Since(start)

			if code != 0 {
				t.Errorf("Post() = %d, want 0 — a non-zero exit from a pre-tool hook is decision \"deny\" on gemini-cli and Copilot, which breaks the user's tool call", code)
			}
			// Bounded, unconditionally: the agent awaits this process, so a row
			// that exits 0 after 60s has still stalled the user's prompt.
			if elapsed > 2*time.Second {
				t.Errorf("Post() took %s, want well under the 2s ceiling — every gemini-cli fire site awaits the hook", elapsed)
			}
			assertReason(t, tt.want, tt.anyOf, &stderr)
		})
	}
}

// assertReason checks the refusal was named, counted and reported — an
// uncounted refusal is indistinguishable from no refusal.
func assertReason(t *testing.T, want Reason, anyOf []Reason, stderr *bytes.Buffer) {
	t.Helper()
	got := Reasons()

	if len(anyOf) > 0 {
		for _, candidate := range anyOf {
			if got[candidate] == 1 {
				return
			}
		}
		t.Errorf("Reasons() = %v, want exactly one of %v", got, anyOf)
		return
	}

	if want == ReasonNone {
		if n := ReasonCount(); n != 0 {
			t.Errorf("ReasonCount() = %d after a delivered payload, want 0 (reasons: %v)", n, got)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q after a delivered payload, want silence", stderr.String())
		}
		return
	}

	if got[want] != 1 {
		t.Errorf("Reasons()[%q] = %d, want 1 (full snapshot: %v)", want, got[want], got)
	}
	if n := ReasonCount(); n != 1 {
		t.Errorf("ReasonCount() = %d, want exactly 1", n)
	}
	if !strings.Contains(stderr.String(), string(want)) {
		t.Errorf("stderr = %q, want it to name the reason %q", stderr.String(), want)
	}
}

func writeAddrFile(t *testing.T, home, addr string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "irrlichd.addr"), []byte(addr+"\n"), 0o600); err != nil {
		t.Fatalf("writing addr file: %v", err)
	}
}

// TestPostResolvesTheDaemonAddressAtFireTime is requirement 4 of #1373: the
// address is read when the hook fires, not when the entry was installed.
//
// The test proves it the only way that distinguishes the two — the daemon does
// not exist yet when the Options are built, and publishes its address afterwards.
// An implementation that captured the address earlier cannot pass this, and an
// installed entry that carried a port could not either, which is the whole
// structural argument: the #1178 stale-port class stops being expressible.
func TestPostResolvesTheDaemonAddressAtFireTime(t *testing.T) {
	resetCounts()
	home := t.TempDir()
	t.Setenv("IRRLICHT_HOME", home)
	t.Setenv(daemonaddr.EnvBindAddr, "")

	opts := Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader(validPayload)}

	// Only now does a daemon come up and publish where it landed.
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	writeAddrFile(t, home, strings.TrimPrefix(srv.URL, "http://"))

	if code := Post(opts); code != 0 {
		t.Fatalf("Post() = %d, want 0", code)
	}
	select {
	case path := <-got:
		if path != EndpointPath("gemini-cli") {
			t.Errorf("posted to %q, want %q", path, EndpointPath("gemini-cli"))
		}
	default:
		t.Fatalf("the daemon that published its address after the beacon was built never received the payload (reasons: %v)", Reasons())
	}
}

// TestEnvBeatsTheAddrFile locks daemonaddr's documented precedence through the
// beacon: an explicit IRRLICHT_BIND_ADDR is the caller naming the daemon it
// means, and the addr file can outlive a SIGKILLed daemon. This is a LOCK on
// existing daemonaddr behaviour, not a regression test — it passes on main by
// construction and is here so a future "just read the file" simplification of
// the beacon fails loudly.
func TestEnvBeatsTheAddrFile(t *testing.T) {
	resetCounts()
	home := t.TempDir()
	t.Setenv("IRRLICHT_HOME", home)
	writeAddrFile(t, home, closedPort(t))

	hit := make(chan struct{}, 1)
	serverAt(t, func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		w.WriteHeader(http.StatusOK)
	})

	if code := Post(Options{Args: []string{"codex"}, Stdin: strings.NewReader(validPayload)}); code != 0 {
		t.Fatalf("Post() = %d, want 0", code)
	}
	select {
	case <-hit:
	default:
		t.Errorf("the env-named daemon was not reached; the addr file won (reasons: %v)", Reasons())
	}
}

// TestPostSendsTheBodyVerbatim pins that the beacon is a pipe, not a parser. It
// stamps the adapter through the URL path and forwards the bytes untouched, so a
// field the daemon has not been taught about yet cannot be dropped in transit by
// a decode/re-encode round trip (#1371).
func TestPostSendsTheBodyVerbatim(t *testing.T) {
	resetCounts()
	type received struct {
		method, path, contentType, body string
	}
	seen := make(chan received, 1)
	serverAt(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- received{r.Method, r.URL.Path, r.Header.Get("Content-Type"), string(body)}
		w.WriteHeader(http.StatusOK)
	})

	const payload = `{"hook_event_name":"BeforeTool","an_unknown_future_field":{"a":[1,2,3]}}`
	if code := Post(Options{Args: []string{"gemini-cli", LegacyGuardToken}, Stdin: strings.NewReader(payload)}); code != 0 {
		t.Fatalf("Post() = %d, want 0", code)
	}

	r := <-seen
	if r.method != http.MethodPost {
		t.Errorf("method = %q, want POST", r.method)
	}
	if r.path != "/api/v1/hooks/gemini-cli" {
		t.Errorf("path = %q, want /api/v1/hooks/gemini-cli", r.path)
	}
	if r.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", r.contentType)
	}
	if r.body != payload {
		t.Errorf("body = %q, want it byte-identical to stdin %q", r.body, payload)
	}
}

// TestOptionsHasNoStdout is a structural lock, not a behavioural test: a hook's
// stdout is a decision channel (gemini-cli and Claude Code both parse JSON from
// it), and the guarantee that the beacon never writes there is that it does not
// hold the handle. Adding a Stdout field would quietly reopen that.
func TestOptionsHasNoStdout(t *testing.T) {
	if _, found := reflect.TypeOf(Options{}).FieldByName("Stdout"); found {
		t.Error("Options grew a Stdout field — a hook's stdout is a decision channel the beacon must never write to; see the package doc")
	}
}

// TestReasonsSnapshotIsACopy — handing out the live counters lets a reader
// corrupt them.
func TestReasonsSnapshotIsACopy(t *testing.T) {
	resetCounts()
	count(ReasonDaemonUnreachable)
	count(ReasonDaemonUnreachable)

	snap := Reasons()
	if snap[ReasonDaemonUnreachable] != 2 {
		t.Fatalf("Reasons()[daemon_unreachable] = %d, want 2", snap[ReasonDaemonUnreachable])
	}
	snap[ReasonDaemonUnreachable] = 99
	if Reasons()[ReasonDaemonUnreachable] != 2 {
		t.Error("Reasons() handed out the live counters")
	}
	if n := ReasonCount(); n != 2 {
		t.Errorf("ReasonCount() = %d, want 2", n)
	}
}

// TestReasonsOmitsZeros mirrors hookjson's accessors: an absent key means it
// never happened, so a snapshot full of zeros cannot be mistaken for evidence.
func TestReasonsOmitsZeros(t *testing.T) {
	resetCounts()
	count(ReasonEmptyPayload)
	if snap := Reasons(); len(snap) != 1 {
		t.Errorf("Reasons() = %v, want exactly one non-zero entry", snap)
	}
}

// TestEveryReasonHasACounterSlot guards the one way this idiom breaks silently:
// a reason added to the constant block but not to the reasons array is counted
// by nobody, and its refusal becomes invisible.
func TestEveryReasonHasACounterSlot(t *testing.T) {
	resetCounts()
	all := []Reason{
		ReasonMissingAdapter, ReasonInvalidAdapter, ReasonUnreadableStdin,
		ReasonEmptyPayload, ReasonPayloadTooLarge, ReasonDaemonUnreachable,
		ReasonDeadlineExceeded, ReasonDaemonRejected,
	}
	if len(all) != len(reasons) {
		t.Fatalf("this test lists %d reasons but the counter table has %d — add the new one to both", len(all), len(reasons))
	}
	for _, r := range all {
		resetCounts()
		count(r)
		if got := Reasons()[r]; got != 1 {
			t.Errorf("count(%q) did not land in a counter slot", r)
		}
	}
}

func TestIsInvocation(t *testing.T) {
	tests := map[string]struct {
		args []string
		want bool
	}{
		"the verb alone":                  {[]string{"hook-post"}, true},
		"verb and adapter":                {[]string{"hook-post", "gemini-cli"}, true},
		"verb, adapter and guard token":   {[]string{"hook-post", "gemini-cli", "--version"}, true},
		"no arguments":                    {nil, false},
		"a flag":                          {[]string{"--record"}, false},
		"the verb in a later position":    {[]string{"--record", "hook-post"}, false},
		"a value that merely contains it": {[]string{"--watch=hook-post"}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := IsInvocation(tt.args); got != tt.want {
				t.Errorf("IsInvocation(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestEndpointPath(t *testing.T) {
	if got := EndpointPath("gemini-cli"); got != "/api/v1/hooks/gemini-cli" {
		t.Errorf("EndpointPath = %q, want /api/v1/hooks/gemini-cli", got)
	}
}

// TestDefaultTimeoutIsShort pins the ceiling rather than the exact value. Every
// gemini-cli fire site awaits the hook and its config default is 60s; the Phase C
// audit measured a 5s hook delaying the approval dialog by 5.24s, so the cost of
// a large value here is paid in full, in the foreground, on the user's prompt.
func TestDefaultTimeoutIsShort(t *testing.T) {
	if DefaultTimeout > 2*time.Second {
		t.Errorf("DefaultTimeout = %s; a hook the agent awaits must not be able to stall a prompt this long", DefaultTimeout)
	}
}

func TestAdapterSegmentRejectsUnsafeValues(t *testing.T) {
	bad := []string{
		"", "..", "../codex", "code x", "Codex", "codex/", "/codex", "codex?x=1",
		"codex#f", "codex%2e", "-codex", "codex-", "co\nex", strings.Repeat("a", 65),
	}
	for _, s := range bad {
		if adapterSegment.MatchString(s) {
			t.Errorf("adapter segment %q was accepted; it must not be pasted into a URL path", s)
		}
	}
	for _, s := range []string{"codex", "claudecode", "gemini-cli", "copilot", "aider", "kiro-cli", "a", "a1"} {
		if !adapterSegment.MatchString(s) {
			t.Errorf("adapter segment %q was rejected; it is a real adapter id", s)
		}
	}
}

// TestReportNeverPanicsWithoutStderr — a CLI is free to give the hook no stderr.
func TestReportNeverPanicsWithoutStderr(t *testing.T) {
	resetCounts()
	t.Setenv(daemonaddr.EnvBindAddr, closedPort(t))
	if code := Post(Options{Args: []string{"gemini-cli"}, Stdin: strings.NewReader(validPayload), Stderr: nil}); code != 0 {
		t.Fatalf("Post() = %d, want 0", code)
	}
	if n := ReasonCount(); n != 1 {
		t.Errorf("ReasonCount() = %d, want 1 — the counter must move even when there is nowhere to report", n)
	}
}
