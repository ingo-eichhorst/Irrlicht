package processlifecycle

import (
	"testing"

	"irrlicht/core/domain/session"
)

// routeSpyObserver answers EnvOf from a fixed map/error and records whether
// it was ever called — the "denied ⇒ no read happens" fixture (#2002 §6)
// needs proof that the read was skipped, not merely that the answer looks
// like denial.
type routeSpyObserver struct {
	fakeObserver
	env         map[string]string
	err         error
	calledEnvOf bool
}

func (o *routeSpyObserver) EnvOf(int, map[string]struct{}) (map[string]string, error) {
	o.calledEnvOf = true
	return o.env, o.err
}

func withRouteObserver(t *testing.T, o *routeSpyObserver) {
	t.Helper()
	prev := osProc
	osProc = o
	t.Cleanup(func() { osProc = prev })
}

// TestObserveRoute_Denied pins #2002 §6's "denied" row: no read happens, and
// the result IS denied — not silently absent.
func TestObserveRoute_Denied(t *testing.T) {
	spy := &routeSpyObserver{env: map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:8080/v1"}}
	withRouteObserver(t, spy)

	got := ObserveRoute(123, false)

	if spy.calledEnvOf {
		t.Error("a denied permission must not read the environment at all")
	}
	if got == nil || got.Status != session.RouteDenied {
		t.Errorf("got %+v, want Status=denied", got)
	}
}

// TestObserveRoute_Unreadable pins the "unreadable" row: EnvOf returning a
// non-nil error is reported as unreadable, distinct from absent.
func TestObserveRoute_Unreadable(t *testing.T) {
	withRouteObserver(t, &routeSpyObserver{err: errEnvOfFailedForTest})

	got := ObserveRoute(123, true)

	if got == nil || got.Status != session.RouteUnreadable {
		t.Errorf("got %+v, want Status=unreadable", got)
	}
}

// TestObserveRoute_Absent pins the "readable, no override key" row: a
// granted, successful read that names none of endpointEnvKeys is absent —
// never "the default provider".
func TestObserveRoute_Absent(t *testing.T) {
	withRouteObserver(t, &routeSpyObserver{env: map[string]string{"TERM_PROGRAM": "iTerm.app"}})

	got := ObserveRoute(123, true)

	if got == nil || got.Status != session.RouteAbsent {
		t.Errorf("got %+v, want Status=absent", got)
	}
	if got.Endpoint != "" {
		t.Errorf("absent must carry no endpoint value, got %q", got.Endpoint)
	}
}

// TestObserveRoute_Malformed pins the "malformed" row, and that a malformed
// value's possible userinfo is never returned.
func TestObserveRoute_Malformed(t *testing.T) {
	withRouteObserver(t, &routeSpyObserver{env: map[string]string{
		"ANTHROPIC_BASE_URL": "not a url",
	}})

	got := ObserveRoute(123, true)

	if got == nil || got.Status != session.RouteMalformed {
		t.Errorf("got %+v, want Status=malformed", got)
	}
	if got.Endpoint != "" {
		t.Errorf("malformed must carry no endpoint value, got %q", got.Endpoint)
	}
	if got.Source == "" {
		t.Error("malformed should still name which key was found, even though its value didn't parse")
	}
}

// TestObserveRoute_ObservedAndRedacted pins the "observed" row end to end:
// userinfo, query, and fragment stripped; scheme/host/port/path survive.
func TestObserveRoute_ObservedAndRedacted(t *testing.T) {
	withRouteObserver(t, &routeSpyObserver{env: map[string]string{
		"ANTHROPIC_BASE_URL": "https://user:pass@api.example.com:8443/v1/base?key=secret#frag",
	}})

	got := ObserveRoute(123, true)

	if got == nil || got.Status != session.RouteObserved {
		t.Fatalf("got %+v, want Status=observed", got)
	}
	want := "https://api.example.com:8443/v1/base"
	if got.Endpoint != want {
		t.Errorf("Endpoint = %q, want %q (userinfo/query/fragment stripped, path kept)", got.Endpoint, want)
	}
	if got.Source != "env:ANTHROPIC_BASE_URL" {
		t.Errorf("Source = %q, want env:ANTHROPIC_BASE_URL", got.Source)
	}
	if got.Local {
		t.Error("api.example.com is not a local address")
	}
}

// TestObserveRoute_LocalDoesNotMeanLocalInferenceOrFreeCost pins the
// 127.0.0.1 fixture row: Local is set, but nothing else about the value
// changes — no separate "free"/"local inference" status exists.
func TestObserveRoute_LocalDoesNotMeanLocalInferenceOrFreeCost(t *testing.T) {
	withRouteObserver(t, &routeSpyObserver{env: map[string]string{
		"ANTHROPIC_BASE_URL": "http://127.0.0.1:8080/v1",
	}})

	got := ObserveRoute(123, true)

	if got == nil || got.Status != session.RouteObserved {
		t.Fatalf("got %+v, want Status=observed", got)
	}
	if !got.Local {
		t.Error("127.0.0.1 must be reported Local")
	}
	if got.Endpoint != "http://127.0.0.1:8080/v1" {
		t.Errorf("Endpoint = %q, want http://127.0.0.1:8080/v1 (no query/fragment to strip here)", got.Endpoint)
	}
}

// errEnvOfFailedForTest is a sentinel error standing in for any real
// unreadable-environment failure (process gone, permission denied, a hidden
// darwin env) — ObserveRoute must react to err != nil uniformly, regardless
// of its origin.
var errEnvOfFailedForTest = errUnreadableEnvForTest{}

type errUnreadableEnvForTest struct{}

func (errUnreadableEnvForTest) Error() string { return "env unreadable (test double)" }

// --- redactEndpoint unit coverage ---

func TestRedactEndpoint(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantOK       bool
		wantEndpoint string
		wantLocal    bool
	}{
		{
			name:         "strips userinfo, query, and fragment; keeps path",
			raw:          "https://user:pass@api.example.com/v1/base?key=secret#frag",
			wantOK:       true,
			wantEndpoint: "https://api.example.com/v1/base",
		},
		{
			name:         "path prefix survives untouched",
			raw:          "https://api.example.com:9000/custom/prefix",
			wantOK:       true,
			wantEndpoint: "https://api.example.com:9000/custom/prefix",
		},
		{
			name:         "loopback with port is local",
			raw:          "http://127.0.0.1:8080/v1",
			wantOK:       true,
			wantEndpoint: "http://127.0.0.1:8080/v1",
			wantLocal:    true,
		},
		{
			// A LiteLLM-style local proxy is commonly addressed by name
			// rather than by IP literal — localhost is reserved to loopback
			// by RFC 6761 §6.3 and needs no DNS resolution to classify.
			name:         "localhost by name is local",
			raw:          "http://localhost:4000/v1",
			wantOK:       true,
			wantEndpoint: "http://localhost:4000/v1",
			wantLocal:    true,
		},
		{
			name:         "localhost is case-insensitive",
			raw:          "http://LOCALHOST:4000/v1",
			wantOK:       true,
			wantEndpoint: "http://LOCALHOST:4000/v1",
			wantLocal:    true,
		},
		{
			name:         "a name under the .localhost TLD is local",
			raw:          "http://proxy.localhost:4000/v1",
			wantOK:       true,
			wantEndpoint: "http://proxy.localhost:4000/v1",
			wantLocal:    true,
		},
		{
			name:         "an ordinary DNS name is not local",
			raw:          "http://api.example.com/v1",
			wantOK:       true,
			wantEndpoint: "http://api.example.com/v1",
			wantLocal:    false,
		},
		{
			name:         "private network address is local",
			raw:          "http://10.0.0.5:11434/v1",
			wantOK:       true,
			wantEndpoint: "http://10.0.0.5:11434/v1",
			wantLocal:    true,
		},
		{
			name:   "no scheme is malformed",
			raw:    "api.example.com/v1",
			wantOK: false,
		},
		{
			name:   "garbage is malformed",
			raw:    "not a url",
			wantOK: false,
		},
		{
			name:   "scheme with no host is malformed",
			raw:    "http://",
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, local, ok := redactEndpoint(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (endpoint=%q)", ok, tc.wantOK, endpoint)
			}
			if !ok {
				if endpoint != "" {
					t.Errorf("a malformed value must never return a non-empty endpoint (possible userinfo leak), got %q", endpoint)
				}
				return
			}
			if endpoint != tc.wantEndpoint {
				t.Errorf("endpoint = %q, want %q", endpoint, tc.wantEndpoint)
			}
			if local != tc.wantLocal {
				t.Errorf("local = %v, want %v", local, tc.wantLocal)
			}
		})
	}
}
