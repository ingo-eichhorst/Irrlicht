// Command irrlichtrelay is the standalone relay server: daemons push their
// session events to it over a WebSocket, and it fans them out to connected
// macOS/web clients while re-serving the dashboard and the daemon's HTTP API
// (/api/v1/sessions, /api/v1/agents, /api/v1/version) from an in-memory cache.
//
// v0 is deliberately thin: single node, in-memory only, no auth, no TLS, no
// persistence. State is rebuilt from each daemon's reconnect daemon_snapshot.
// See docs/relay-protocol.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Version is injected at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

const envUIDir = "IRRLICHT_UI_DIR"

// tokenReloadInterval is how often a serving relay re-checks the tokens file's
// mtime so `token revoke` (a separate process) propagates without a restart.
const tokenReloadInterval = 2 * time.Second

func main() {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-v") {
		fmt.Printf("irrlichtrelay version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return
	}
	// Subcommand dispatch: `serve` (the default when the first arg is a flag or
	// absent) runs the relay; `token issue|list|revoke` manages the bearer-token
	// store. Anything else is an error.
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		runServe(args)
	case "token":
		runToken(args)
	default:
		fmt.Fprintf(os.Stderr, "irrlichtrelay: unknown command %q (want serve|token)\n", cmd)
		os.Exit(2)
	}
}

// dataDir is the relay's per-user state directory, mirroring irrlichd's: the
// IRRLICHT_HOME override, else ~/.local/share/irrlicht. It holds the hashed
// tokens file when --auth tokens-file is given without an explicit path.
func dataDir() string {
	if v := os.Getenv("IRRLICHT_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return "/tmp/irrlicht"
	}
	return filepath.Join(home, ".local", "share", "irrlicht")
}

// runServe parses the serve flags and runs the relay until SIGINT/SIGTERM.
func runServe(args []string) {
	fs := flag.NewFlagSet("irrlichtrelay serve", flag.ExitOnError)
	// Loopback by default: a no-auth relay must not be reachable from the LAN
	// unless explicitly asked. Pass --addr 0.0.0.0:7839 to expose it — and only
	// with --auth or behind a TLS-terminating reverse proxy. Mirrors irrlichd.
	addr := fs.String("addr", "127.0.0.1:7839", "TCP address to listen on; defaults to loopback (use 0.0.0.0:7839 to expose on the LAN)")
	tlsCert := fs.String("tls-cert", "", "PEM certificate file for native TLS (wss://); pair with --tls-key")
	tlsKey := fs.String("tls-key", "", "PEM private-key file for native TLS (wss://); pair with --tls-cert")
	auth := fs.String("auth", "off", "authentication: 'off' (trusted LAN, accept any hello) or 'tokens-file[:PATH]' (verify a hashed bearer token; PATH defaults to <data-dir>/tokens.json)")
	originAllowlist := fs.String("origin-allowlist", "", "comma-separated Origin hosts allowed for browser WS clients (empty = allow all, loopback-safe)")
	dataDirFlag := fs.String("data-dir", "", "state directory for the tokens file (default: $IRRLICHT_HOME or ~/.local/share/irrlicht)")
	_ = fs.Parse(args)

	if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatal("--tls-cert and --tls-key must be given together")
	}
	tlsEnabled := *tlsCert != ""

	ddir := *dataDirFlag
	if ddir == "" {
		ddir = dataDir()
	}

	var store *authStore
	switch {
	case *auth == "off":
		// no auth
	case *auth == "tokens-file" || strings.HasPrefix(*auth, "tokens-file:"):
		// TrimPrefix leaves a bare "tokens-file" (no colon) untouched and turns
		// "tokens-file:PATH" into PATH; both the bare form and an empty PATH mean
		// "use the default path".
		path := strings.TrimPrefix(*auth, "tokens-file:")
		if path == "" || path == "tokens-file" {
			path = filepath.Join(ddir, tokensFilename)
		}
		s, err := newAuthStore(path)
		if err != nil {
			log.Fatalf("loading tokens file %s: %v", path, err)
		}
		store = s
		log.Printf("auth enabled: %d token(s) loaded from %s", len(s.hashes), path)
	default:
		log.Fatalf("invalid --auth %q (want off | tokens-file[:PATH])", *auth)
	}

	allowedOrigins := splitCSV(*originAllowlist)

	// Loud warning when exposed without authentication. TLS encrypts the wire
	// but does NOT authenticate the peer, so a non-loopback bind without --auth
	// is wide open regardless of TLS — anyone who can reach it reads every
	// session and can inject as a daemon. (A reverse proxy that terminates TLS
	// still binds the relay on loopback, so this only fires on a real exposure.)
	if !isLoopback(*addr) && store == nil {
		log.Printf("WARNING: binding %s with no --auth — anyone who can reach this address can read every session and inject as a daemon. Enable --auth (TLS alone does not authenticate), or restrict access at the network layer.", *addr)
	}

	// Serve web assets with correct Content-Type regardless of the host OS
	// mime database (matches irrlichd).
	_ = mime.AddExtensionType(".js", "application/javascript")
	_ = mime.AddExtensionType(".css", "text/css")

	h := newHubWithAuth(store, allowedOrigins)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions/stream", h.ServeWS)
	// The data endpoints carry the same session content as the WS stream, so
	// they get the same bearer-token gate when --auth is on; otherwise the WS
	// would be authenticated while a plain `curl /api/v1/sessions` leaked
	// everything. version stays open as an unauthenticated health check.
	mux.HandleFunc("GET /api/v1/sessions", requireToken(store, handleSessions(h)))
	mux.HandleFunc("GET /api/v1/agents", requireToken(store, handleAgents(h)))
	mux.HandleFunc("GET /api/v1/version", handleVersion(Version))

	if uiDir := resolveUIDir(); uiDir != "" {
		log.Printf("serving dashboard from %s", uiDir)
		mux.Handle("/", http.FileServer(http.Dir(uiDir)))
	} else {
		log.Printf("dashboard UI not found — set %s to the directory containing index.html", envUIDir)
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "dashboard UI not found", http.StatusServiceUnavailable)
		})
	}

	stop := make(chan struct{})
	if store != nil {
		go store.watch(stop, tokenReloadInterval)
	}

	// WS connections are hijacked by gorilla after the upgrade, so the only
	// server-level timeout that applies is the header read.
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		scheme := "ws"
		if tlsEnabled {
			scheme = "wss"
		}
		log.Printf("irrlichtrelay %s listening on %s (%s://)", Version, *addr, scheme)
		var err error
		if tlsEnabled {
			err = srv.ListenAndServeTLS(*tlsCert, *tlsKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen on %s failed: %v", *addr, err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Print("shutting down")
	close(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// runToken implements `token issue|list|revoke`, operating directly on the
// hashed tokens file (no running relay required).
func runToken(args []string) {
	if len(args) == 0 {
		log.Fatal("token: want a subcommand (issue|list|revoke)")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("irrlichtrelay token "+sub, flag.ExitOnError)
	tokensFile := fs.String("tokens-file", "", "path to the tokens file (default: <data-dir>/tokens.json)")
	dataDirFlag := fs.String("data-dir", "", "state directory for the tokens file (default: $IRRLICHT_HOME or ~/.local/share/irrlicht)")
	label := fs.String("label", "", "human label for the issued token (issue only)")
	_ = fs.Parse(rest)

	path := *tokensFile
	if path == "" {
		ddir := *dataDirFlag
		if ddir == "" {
			ddir = dataDir()
		}
		path = filepath.Join(ddir, tokensFilename)
	}

	switch sub {
	case "issue":
		id, plaintext, err := issueToken(path, *label)
		if err != nil {
			log.Fatalf("token issue: %v", err)
		}
		fmt.Printf("token %s issued (label %q)\n", id, *label)
		fmt.Printf("  %s\n", plaintext)
		fmt.Println("Store it now — it is shown only once and only its hash is kept.")
	case "list":
		recs, err := sortedRecords(path)
		if err != nil {
			log.Fatalf("token list: %v", err)
		}
		if len(recs) == 0 {
			fmt.Println("no tokens issued")
			return
		}
		for _, r := range recs {
			fmt.Printf("%s\t%s\t%s\n", r.ID, time.Unix(r.Created, 0).Format(time.RFC3339), r.Label)
		}
	case "revoke":
		if len(fs.Args()) == 0 {
			log.Fatal("token revoke: want a token id")
		}
		id := fs.Args()[0]
		ok, err := revokeToken(path, id)
		if err != nil {
			log.Fatalf("token revoke: %v", err)
		}
		if !ok {
			log.Fatalf("token revoke: no token with id %q", id)
		}
		fmt.Printf("token %s revoked; the peer's next frame will be closed with %d\n", id, 4401)
	default:
		log.Fatalf("token: unknown subcommand %q (want issue|list|revoke)", sub)
	}
}

// requireToken wraps an HTTP API handler with the bearer-token gate. With auth
// off (store == nil) it is a pass-through, so existing no-auth deployments are
// unchanged. With auth on, the request must carry a valid token in either an
// `Authorization: Bearer <t>` header or a `?token=<t>` query param (the WS
// endpoint authenticates via the hello frame instead and is not wrapped).
func requireToken(store *authStore, next http.HandlerFunc) http.HandlerFunc {
	if store == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := store.validate(bearerToken(r)); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// bearerToken extracts a token from an Authorization: Bearer header, falling
// back to the `token` query param (browsers can't set headers on some requests
// and the dashboard hydrates same-origin).
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.URL.Query().Get("token")
}

// splitCSV trims and drops empties from a comma-separated flag value.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isLoopback reports whether a listen address binds only the loopback
// interface, so the exposure warning fires only on a LAN/public bind.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false // empty host means all interfaces
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// resolveUIDir locates the dashboard's index.html, mirroring irrlichd's search
// order so the relay serves the same three-file dashboard in dev and packaged
// installs: env override → app bundle Resources → daemon curl-install dir →
// walk up from the executable to the repo root for platforms/web/.
func resolveUIDir() string {
	exe, _ := os.Executable()
	home, _ := os.UserHomeDir()
	return resolveUIDirFor(os.Getenv(envUIDir), exe, home)
}

func resolveUIDirFor(env, exe, home string) string {
	hasIndex := func(dir string) bool {
		if dir == "" {
			return false
		}
		_, err := os.Stat(filepath.Join(dir, "index.html"))
		return err == nil
	}
	if hasIndex(env) {
		return env
	}
	if exe != "" {
		if cand := filepath.Join(filepath.Dir(exe), "..", "Resources", "web"); hasIndex(cand) {
			return cand
		}
	}
	if home != "" {
		if cand := filepath.Join(home, ".local", "share", "irrlicht", "web"); hasIndex(cand) {
			return cand
		}
	}
	if exe != "" {
		dir := filepath.Dir(exe)
		for range 8 {
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				if cand := filepath.Join(dir, "platforms", "web"); hasIndex(cand) {
					return cand
				}
				return "" // repo root found, no UI inside — don't escape it
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}
