# otel-spike

A throwaway, dependency-free OTLP/HTTP+JSON sink used to answer issue
[#1141](https://github.com/ingo-eichhorst/Irrlicht/issues/1141): does Claude
Code's OpenTelemetry export carry a usable session-state signal, and how late
does it land? Findings live in [`docs/otel-spike-1141.md`](../../docs/otel-spike-1141.md).

It is **not** a component of irrlichd — it is a measurement harness. It lives in
its own module (`irrlicht/tools/otel-spike`, stdlib only) so no CI gate builds or
tests it as part of the daemon.

## What it does

Listens on `/v1/traces`, `/v1/logs`, `/v1/metrics` and, for every span and log
record, prints a one-line summary with the **end-to-end latency** it observed:
`(wall-clock receive time) − (the event's own timestamp)`. Both timestamps come
from the same host, so the delta is a direct measurement of how long after a
thing happened a local receiver would learn about it. Raw bodies are written
verbatim to `-capture` as evidence.

## Run

```bash
go build -o /tmp/otel-spike ./tools/otel-spike
/tmp/otel-spike -addr :4318 -capture /tmp/otel-cap
```

Then start Claude Code pointed at it. The two easy-to-miss env vars are the
master switch and the JSON protocol (without `http/json` the payloads arrive as
protobuf, which this sink does not decode):

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1          # master switch — nothing exports without it
export CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1   # enables the trace spans (blocked_on_user, interaction)
export OTEL_LOGS_EXPORTER=otlp
export OTEL_TRACES_EXPORTER=otlp
export OTEL_METRICS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/json   # so this sink can read it
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
export OTEL_LOGS_EXPORT_INTERVAL=1000          # 1s floor; default is 5000
claude
```

To force a permission prompt (so `blocked_on_user` is exercised) regardless of
your personal allowlist, drop a project `.claude/settings.json` next to where you
launch `claude`:

```json
{ "permissions": { "defaultMode": "default", "ask": ["Bash", "Write", "Edit"] } }
```

## Note

Claude Code's OTLP/JSON encodes some `intValue` attributes as bare JSON numbers
rather than the spec-mandated strings; the sink accepts both. Payloads carry PII
(`user.email`, `organization.id`, …) — redact before sharing captures.
