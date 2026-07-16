// Command otel-spike is a throwaway OTLP/HTTP+JSON sink for issue #1141: it
// measures whether Claude Code's OpenTelemetry export carries a usable
// state-detection signal, and how late that signal lands.
//
// It is deliberately dependency-free (stdlib only) and lives outside the
// irrlicht/core module so no CI gate touches it. Claude Code must be run with
// OTEL_EXPORTER_OTLP_PROTOCOL=http/json so the payloads arrive as JSON rather
// than protobuf — see README.md for the exact env block.
//
// For every span and log record received, the sink prints a one-line summary
// with the end-to-end latency it observed: (wall-clock receive time) minus (the
// event's own timestamp from the payload). Both timestamps come from the same
// host, so the delta is a direct measurement of "how long after the thing
// happened would irrlichd learn about it via OTel". Raw bodies are also written
// verbatim to the capture dir as evidence.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// --- Minimal OTLP/JSON shapes. We decode only the fields the spike needs and
// ignore everything else; unknown fields are dropped by encoding/json. ---

type anyValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	// The OTLP/JSON spec says int64 is encoded as a string, but Claude Code's
	// exporter sends some intValue attributes as bare JSON numbers. RawMessage
	// accepts both ("123" and 123) — decoding it as *string would fail the whole
	// payload and drop every attribute in the record.
	IntValue    json.RawMessage `json:"intValue,omitempty"`
	BoolValue   *bool           `json:"boolValue,omitempty"`
	DoubleValue *float64        `json:"doubleValue,omitempty"`
}

func (v anyValue) String() string {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case len(v.IntValue) > 0:
		return strings.Trim(string(v.IntValue), `"`)
	case v.BoolValue != nil:
		return strconv.FormatBool(*v.BoolValue)
	case v.DoubleValue != nil:
		return strconv.FormatFloat(*v.DoubleValue, 'f', -1, 64)
	default:
		return ""
	}
}

type kv struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

func attrMap(attrs []kv) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Value.String()
	}
	return m
}

type span struct {
	Name              string `json:"name"`
	StartTimeUnixNano string `json:"startTimeUnixNano"`
	EndTimeUnixNano   string `json:"endTimeUnixNano"`
	Attributes        []kv   `json:"attributes"`
}

type logRecord struct {
	TimeUnixNano string   `json:"timeUnixNano"`
	Body         anyValue `json:"body"`
	Attributes   []kv     `json:"attributes"`
}

type tracesPayload struct {
	ResourceSpans []struct {
		ScopeSpans []struct {
			Spans []span `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type logsPayload struct {
	ResourceLogs []struct {
		ScopeLogs []struct {
			LogRecords []logRecord `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

type metricsPayload struct {
	ResourceMetrics []struct {
		ScopeMetrics []struct {
			Metrics []struct {
				Name string `json:"name"`
			} `json:"metrics"`
		} `json:"scopeMetrics"`
	} `json:"resourceMetrics"`
}

var seq int64

func main() {
	addr := flag.String("addr", ":4318", "listen address for the OTLP/HTTP sink")
	captureDir := flag.String("capture", "", "directory to write raw request bodies to (optional)")
	flag.Parse()

	if *captureDir != "" {
		if err := os.MkdirAll(*captureDir, 0o755); err != nil {
			log.Fatalf("cannot create capture dir: %v", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", handler("TRACES", *captureDir))
	mux.HandleFunc("/v1/logs", handler("LOGS", *captureDir))
	mux.HandleFunc("/v1/metrics", handler("METRICS", *captureDir))
	// Catch-all so a misconfigured path is visible rather than a silent 404.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Printf("%s  OTHER   path=%s bytes=%d\n", now(), r.URL.Path, len(body))
		w.WriteHeader(http.StatusOK)
	})

	fmt.Printf("%s  otel-spike listening on %s (capture=%q)\n", now(), *addr, *captureDir)
	fmt.Printf("%s  columns: RECV_TIME  SIGNAL  name  event_ts  latency_ms  {attrs}\n", now())
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func handler(signal, captureDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recv := time.Now()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// OTLP wants an empty-ish success response; the CLI ignores the body.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))

		n := atomic.AddInt64(&seq, 1)
		if captureDir != "" {
			fn := filepath.Join(captureDir, fmt.Sprintf("%04d-%s.json", n, signal))
			_ = os.WriteFile(fn, body, 0o644)
		}

		switch signal {
		case "TRACES":
			reportTraces(recv, body)
		case "LOGS":
			reportLogs(recv, body)
		case "METRICS":
			reportMetrics(recv, body)
		}
	}
}

func reportTraces(recv time.Time, body []byte) {
	var p tracesPayload
	if err := json.Unmarshal(body, &p); err != nil {
		fmt.Printf("%s  TRACES  <unparseable: %v>\n", ts(recv), err)
		return
	}
	for _, rs := range p.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				lat := latencyMs(recv, s.EndTimeUnixNano)
				fmt.Printf("%s  TRACES  %-38s end=%s  lat=%8.1fms  %v\n",
					ts(recv), s.Name, shortNano(s.EndTimeUnixNano), lat, attrMap(s.Attributes))
			}
		}
	}
}

func reportLogs(recv time.Time, body []byte) {
	var p logsPayload
	if err := json.Unmarshal(body, &p); err != nil {
		fmt.Printf("%s  LOGS  <unparseable: %v>\n", ts(recv), err)
		return
	}
	for _, rl := range p.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				lat := latencyMs(recv, lr.TimeUnixNano)
				attrs := attrMap(lr.Attributes)
				name := attrs["event.name"]
				if name == "" {
					name = lr.Body.String()
				}
				fmt.Printf("%s  LOGS    %-38s ts=%s  lat=%8.1fms  %v\n",
					ts(recv), name, shortNano(lr.TimeUnixNano), lat, attrs)
			}
		}
	}
}

func reportMetrics(recv time.Time, body []byte) {
	var p metricsPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return
	}
	var names []string
	for _, rm := range p.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				names = append(names, m.Name)
			}
		}
	}
	fmt.Printf("%s  METRICS %d metrics %v\n", ts(recv), len(names), names)
}

// latencyMs returns milliseconds between the sink's receive time and the
// event's own timestamp (nanoseconds since epoch, as an OTLP/JSON string).
// Same host → same clock, so this is a real end-to-end latency. Returns -1 if
// the timestamp is missing or unparseable.
func latencyMs(recv time.Time, nano string) float64 {
	if nano == "" {
		return -1
	}
	n, err := strconv.ParseInt(nano, 10, 64)
	if err != nil || n == 0 {
		return -1
	}
	evt := time.Unix(0, n)
	return float64(recv.Sub(evt).Microseconds()) / 1000.0
}

func shortNano(nano string) string {
	n, err := strconv.ParseInt(nano, 10, 64)
	if err != nil || n == 0 {
		return "-"
	}
	return time.Unix(0, n).Format("15:04:05.000")
}

func now() string           { return ts(time.Now()) }
func ts(t time.Time) string { return t.Format("15:04:05.000") }
