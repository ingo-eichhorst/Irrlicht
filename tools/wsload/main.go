// Command wsload is a throwaway CPU-measurement harness for issue #690.
//
// It impersonates just enough of the irrlichd HTTP+WebSocket surface to drive
// the macOS app under a realistic many-agent load, so app CPU can be measured
// before/after the session-update coalescing change. It serves:
//
//	GET /api/v1/agents          -> [] (no custom branding)
//	GET /api/v1/permissions     -> 404 (pre-#570 shape; app leaves snapshot nil)
//	GET /api/v1/sessions        -> N working sessions spread across -groups groups
//	GET /api/v1/sessions/stream -> a session_updated storm at -rate pushes/sec
//
// Usage:
//
//	go run . -port 7838 -sessions 40 -rate 200
//	# then launch the app pointed at the harness:
//	IRRLICHT_DAEMON_PORT=7838 open -n /path/to/Irrlicht.app
//	# measure steady-state app CPU, e.g.:
//	ps -o %cpu,rss,comm -p "$(pgrep -x Irrlicht)"
//
// The push storm keeps every session in the "working" state and only mutates
// metrics (cost climbs, context oscillates) — i.e. the exact pure-metric churn
// that used to re-render the whole list once per message. Compare CPU on `main`
// vs `fix/690-macos-cpu` at the same -sessions/-rate to quantify the fix.
package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var (
	port     = flag.Int("port", 7838, "port to listen on (point the app at it via IRRLICHT_DAEMON_PORT)")
	sessions = flag.Int("sessions", 40, "number of working sessions to advertise")
	groups   = flag.Int("groups", 4, "number of project groups to spread sessions across")
	rate     = flag.Int("rate", 200, "total session_updated pushes per second across all sessions")
)

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// seq is the daemon-global monotonic push counter the app uses for slow-drop
// detection (#593). Incrementing it by 1 per push keeps the app from triggering
// a re-hydration on a perceived gap.
var seq uint64

func main() {
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[]")
	})
	mux.HandleFunc("/api/v1/permissions", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	mux.HandleFunc("/api/v1/sessions", handleSessions)
	mux.HandleFunc("/api/v1/sessions/stream", handleStream)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("wsload: %d sessions across %d groups, %d pushes/sec on http://%s", *sessions, *groups, *rate, addr)
	log.Printf("wsload: launch the app with IRRLICHT_DAEMON_PORT=%d", *port)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func projectName(i int) string { return fmt.Sprintf("proj%d", i%*groups) }

func sessionID(i int) string { return fmt.Sprintf("wsload-%03d", i) }

// sessionJSON renders one session object. cost/ctx vary per push so the app
// does real diff/render work; everything else is stable.
func sessionJSON(i int, cost, ctx float64, now int64) string {
	return fmt.Sprintf(
		`{"session_id":"%s","state":"working","model":"claude-opus-4-8","cwd":"/tmp/%s",`+
			`"project_name":"%s","first_seen":%d,"updated_at":%d,`+
			`"metrics":{"elapsed_seconds":%d,"total_tokens":%d,"model_name":"claude-opus-4-8",`+
			`"context_window":200000,"context_utilization_percentage":%.2f,"pressure_level":"safe",`+
			`"estimated_cost_usd":%.4f}}`,
		sessionID(i), projectName(i), projectName(i),
		now-300, now, int64(300), int64(ctx*2000), ctx, cost,
	)
}

// handleSessions returns the initial hierarchy so the app's apiGroups (the
// list-view surface) is fully populated — without this the WS updates would
// miss the patch guard and only the flat surface would exercise.
func handleSessions(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().Unix()
	byGroup := make([][]string, *groups)
	for i := 0; i < *sessions; i++ {
		g := i % *groups
		byGroup[g] = append(byGroup[g], sessionJSON(i, 0.01, 5, now))
	}
	groupsJSON := ""
	for g := 0; g < *groups; g++ {
		if len(byGroup[g]) == 0 {
			continue
		}
		if groupsJSON != "" {
			groupsJSON += ","
		}
		agents := ""
		for j, s := range byGroup[g] {
			if j > 0 {
				agents += ","
			}
			agents += s
		}
		groupsJSON += fmt.Sprintf(`{"name":"proj%d","agents":[%s]}`, g, agents)
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"groups":[%s],"provider_costs":{}}`, groupsJSON)
}

// handleStream upgrades to WebSocket and pushes a round-robin session_updated
// storm at the configured rate until the client disconnects.
func handleStream(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("wsload: upgrade failed: %v", err)
		return
	}
	defer conn.Close()
	log.Printf("wsload: client connected — streaming")

	// Reader pump: discard inbound frames, detect close.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	interval := time.Second / time.Duration(max(*rate, 1))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var pushes uint64
	logEvery := time.NewTicker(5 * time.Second)
	defer logEvery.Stop()

	i := 0
	for {
		select {
		case <-done:
			log.Printf("wsload: client disconnected after %d pushes", atomic.LoadUint64(&pushes))
			return
		case <-logEvery.C:
			log.Printf("wsload: %d pushes sent", atomic.LoadUint64(&pushes))
		case <-ticker.C:
			now := time.Now().Unix()
			n := atomic.AddUint64(&seq, 1)
			// Cost climbs slowly; context oscillates 5–95% so percentage and
			// pressure rendering churn like a real session.
			cost := 0.01 + float64(n)*0.0001
			ctx := 50 + 45*math.Sin(float64(n)/20)
			env := fmt.Sprintf(`{"type":"session_updated","seq":%d,"session":%s}`,
				n, sessionJSON(i, cost, ctx, now))
			if err := conn.WriteMessage(websocket.TextMessage, []byte(env)); err != nil {
				log.Printf("wsload: write failed: %v", err)
				return
			}
			atomic.AddUint64(&pushes, 1)
			i = (i + 1) % *sessions
		}
	}
}
