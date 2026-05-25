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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// Version is injected at build time via -ldflags "-X main.Version=x.y.z".
var Version = "dev"

const envUIDir = "IRRLICHT_UI_DIR"

func main() {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-v") {
		fmt.Printf("irrlichtrelay version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return
	}
	// Accept an optional "serve" subcommand for forward-compatibility with
	// later verbs (e.g. token issue); v0 has only serve.
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("irrlichtrelay", flag.ExitOnError)
	addr := fs.String("addr", ":7838", "TCP address to listen on (e.g. :7838)")
	_ = fs.Parse(args)

	// Serve web assets with correct Content-Type regardless of the host OS
	// mime database (matches irrlichd).
	_ = mime.AddExtensionType(".js", "application/javascript")
	_ = mime.AddExtensionType(".css", "text/css")

	h := newHub()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions/stream", h.ServeWS)
	mux.HandleFunc("GET /api/v1/sessions", handleSessions(h))
	mux.HandleFunc("GET /api/v1/agents", handleAgents(h))
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

	// WS connections are hijacked by gorilla after the upgrade, so the only
	// server-level timeout that applies is the header read.
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("irrlichtrelay %s listening on %s", Version, *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen on %s failed: %v", *addr, err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Print("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
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
