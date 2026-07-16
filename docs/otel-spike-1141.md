# OTel spike — `claude_code.tool.blocked_on_user` fires, but retrospectively

**Issue:** [#1141](https://github.com/ingo-eichhorst/Irrlicht/issues/1141) · de-risks **Phase 2 of [#1129](https://github.com/ingo-eichhorst/Irrlicht/issues/1129)**
**Date:** 2026-07-16 · **Claude Code:** 2.1.210 · **Host:** macOS (darwin/arm64)
**Harness:** [`tools/otel-spike`](../tools/otel-spike) (throwaway stdlib OTLP/HTTP+JSON sink)

## Verdict

**OTel should NOT be promoted to the primary waiting-vs-ready signal.** The single most-wanted signal in #1129 — `claude_code.tool.blocked_on_user` — fires reliably and cheaply, but it is **retrospective**: it is emitted at the *decision instant* carrying the block as a completed fact (`duration_ms`), and is never delivered while the user is actually blocked. It cannot drive a live "user is waiting now" badge.

OTel's real, bankable value is elsewhere, and it is real:

| Use | Verdict | Evidence |
|---|---|---|
| Live **waiting-entry** signal (retire `waiting_cue`, drive the badge) | ❌ **No** — retrospective, delivered only after the wait ends | `blocked_on_user` endTime == decision time; nothing arrived during a 98 s open prompt |
| **Turn-done → ready** | ✅ Yes, ~1 s | `claude_code.interaction` root span closes at turn end, exports ~1 s later |
| **Cost / tokens / model** (retire transcript parsing) | ✅ Yes | full breakdown on `claude_code.llm_request` / `api_request` |
| **Ordering** behind #150/#988/#996 | ✅ Promising | `interaction.sequence` (spans) + `event.sequence` (logs) are monotonic |
| **Attributed decision history** (who accepted/rejected, why) | ✅ Yes (not live) | `tool_decision` log: `decision`, `source`, `tool_name`, `tool_use_id` |

**Recommendation for the epic:** demote OTel from "primary state source" to **cost/tokens + turn-boundary/ordering + attributed-history source** (a narrowed Phase 4). Keep the live waiting signal on **hooks** (Phase 1). This matches the epic's own #1 risk ("a higher tier that arrives later"), but sharpens it: `blocked_on_user` is not merely *late*, it does not exist until the wait is **over**.

## Kill criteria — answered

The issue set two go/no-go questions. Both are answered, and a third failure mode surfaced that the criteria did not anticipate.

### (a) Does `blocked_on_user` fire on a true permission prompt? Does `interaction` open/close on real turn boundaries?

**Yes to both — with a decisive caveat.** A real permission prompt (forced with a project `ask` rule so the personal allowlist couldn't auto-accept) produced:

- `claude_code.tool.blocked_on_user` — `decision=accept`, `source=user_temporary`, `duration_ms` = the full block. Its `startTimeUnixNano` is block-entry and `endTimeUnixNano` is the decision instant.
- `claude_code.interaction` (root) — opens at prompt submit, closes at turn end; `interaction.duration_ms` spans the whole turn (block included), `interaction.sequence=1`.

**Caveat (the real finding):** a span is exported by the OTel SDK only when it **ends**. `blocked_on_user` ends at the *decision*, so it is delivered ~1 s *after the user stops being blocked* — never during the block. Held a prompt open for **98 s** with zero telemetry arriving the entire time; the span (and the `tool_decision` log) landed ~1 s after the answer, stamped at the decision moment. See [`otel-spike-1141/blocked_on_user.span.json`](./otel-spike-1141/blocked_on_user.span.json).

### (b) Real end-to-end latency at `OTEL_LOGS_EXPORT_INTERVAL=1000`?

**~1.0 s, consistently** — under the 2 s kill threshold. Measured `(sink receive time) − (event's own timestamp)`, same host so clocks align:

| Signal | Measured latency |
|---|---|
| `tool_decision` (log) | 1005 ms |
| `claude_code.tool.blocked_on_user` (span) | 1004 ms |
| `claude_code.tool.execution` (span) | 1004 ms |
| `claude_code.llm_request` (span) | 1004 ms |
| `claude_code.interaction` (root span) | 996 ms |

**But this number is misleading for the waiting question.** ~1 s is the batch-export floor for signals whose *event time equals emit time* (turn-done, cost, the decision *record*). For **waiting-entry**, the event does not exist until the block ends, so the effective latency to "user started waiting" is `block duration + ~1 s` — **effectively unbounded**. The kill criteria measured the wrong latency; the semantic timing of the signal dominates its export latency.

## The A/B: hooks vs OTel for the live waiting signal

To confirm the alternative, a `Notification` (matcher `permission_prompt`) and a `Stop` hook were wired into the child's project settings and timed against the sink.

| Channel | Fires… | Relative to prompt appearing | Live "blocked now"? |
|---|---|---|---|
| OTel `blocked_on_user` span | at decision, exported ~1 s later | **after the block ends** | ❌ no |
| `Notification`/`permission_prompt` hook | **during** the block | **+6.0 s** after the prompt appeared (clean run) | ✅ yes, but delayed |
| `Stop` hook | at true turn end (~4 s after answer) | — | n/a — this is the `ready` signal |

**This corrects the epic's "hooks are a zero-latency push" premise.** The `Notification` family carries a **~6 s idle delay** before firing. It is still the only channel that signals the block *while it is happening* (OTel never does), but it is not instant. The genuinely instant permission signal irrlicht already relies on is the **blocking `PermissionRequest`/`PreToolUse` hook** (it must return before the tool proceeds) — OTel cannot beat that, and `Notification` is for the turn-ended-idle case, not the permission case.

## Implications for #1129

1. **Phase 4 (OTel as production signal) should be re-scoped** from "three adapters resolve state without `waiting_cue`" to "OTel supplies cost/tokens, turn boundaries (`ready`), ordering, and decision history; hooks remain the state authority." OTel does not retire `waiting_cue`; **hooks + the existing permission signal do.**
2. **Phase 2's kill criteria need a third question:** not just "does it fire?" and "how fast does it export?", but **"is the event emitted at block *entry* or block *exit*?"** A signal exported at exit is disqualified for live state no matter how low its export latency.
3. **The port-7837 fixture problem does *not* extend to OTel.** The OTLP endpoint is fully configurable via `OTEL_EXPORTER_OTLP_ENDPOINT`, so a dev daemon on an alt port can receive it — unlike the hooks' hardcoded `localhost:7837`. Point in OTel's favor for the recording story.
4. **Ordering (#150/#988/#996):** `interaction.sequence` + `event.sequence` are present and monotonic — worth a follow-up spike to confirm they linearize the collapsed-turn cases the synthesizers currently patch.
5. **Everything is config-injection, no wrapping** — confirmed. `settings.json` `env` block only; no launcher shim, no `#925` handover cost.

## Method (reproducible)

```bash
go build -o /tmp/otel-spike ./tools/otel-spike
/tmp/otel-spike -addr :4318 -capture /tmp/otel-cap      # terminal 1

# terminal 2 — a Claude Code session wired to the sink (see tools/otel-spike/README.md
# for the full env block; the master switch CLAUDE_CODE_ENABLE_TELEMETRY=1 and
# OTEL_EXPORTER_OTLP_PROTOCOL=http/json are the two easy-to-miss ones):
CLAUDE_CODE_ENABLE_TELEMETRY=1 CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1 \
  OTEL_LOGS_EXPORTER=otlp OTEL_TRACES_EXPORTER=otlp OTEL_METRICS_EXPORTER=otlp \
  OTEL_EXPORTER_OTLP_PROTOCOL=http/json OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
  OTEL_LOGS_EXPORT_INTERVAL=1000 claude
```

To force a permission prompt regardless of the personal allowlist, drop a project
`.claude/settings.json` with `"permissions": {"ask": ["Bash"]}`. Then ask the agent to run a
Bash command and watch the sink: nothing arrives while the prompt is open; `blocked_on_user`
+ `tool_decision` land ~1 s after you answer.

## Gotchas found

- **The master switch is easy to miss.** Without `CLAUDE_CODE_ENABLE_TELEMETRY=1`, nothing exports — the #1129 config list omitted it.
- **OTLP/JSON int encoding is non-conformant.** Claude Code sends some `intValue` attributes as **bare JSON numbers**, not the spec-mandated strings (see `duration_ms` in the evidence). A receiver that types `intValue` as string drops the entire record. `tools/otel-spike` handles both; a real receiver must too.
- **Payloads carry PII** — `user.email`, `user.id`, `organization.id`, `user.account_*`. The committed evidence under `otel-spike-1141/` is redacted. A production ingest path must treat these as sensitive.

## Evidence (redacted)

- [`otel-spike-1141/blocked_on_user.span.json`](./otel-spike-1141/blocked_on_user.span.json) — the retrospective span
- [`otel-spike-1141/interaction.span.json`](./otel-spike-1141/interaction.span.json) — the turn-boundary root span
- [`otel-spike-1141/tool_decision.log.json`](./otel-spike-1141/tool_decision.log.json) — the attributed decision record
