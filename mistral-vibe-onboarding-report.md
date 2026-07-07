# Mistral Vibe onboarding — process report

Onboarding of **Mistral Vibe** (`mistralai/mistral-vibe`, binary `vibe`, v2.19.0)
into irrlicht's scenario × agent fixture matrix, via the `ir:onboarding-factory`
skill. This report documents what was built and every assessment verdict.

/ Everything below is committed on `main`. The AI-generated
`mistral-vibe-irrlicht-adapter-handover.md` was used only as an unverified
starting point — two of its claims were wrong (see "Corrections") and were fixed
against a real `~/.vibe` transcript. /

## What was built

| Step | Result | Commit |
|---|---|---|
| Matrix column registered (`of agent add`) | `mistral-vibe`, provider mistral, min-version 2.19.0 | `72493dff` |
| **Go daemon adapter** (`core/adapters/inbound/agents/vibe/`) | agent.go, parser.go, sidecar.go, pid.go, icons.go, adapter.go + tests | `e943e261` |
| Assess sweep (46 scenarios) | all cells assessed + spec'd | 46 commits |

Adapter shape (verified against a real transcript, not the handover doc):

- **Transport** — `FilesUnderRoot` on `~/.vibe/logs/session/<id>/messages.jsonl`
  (append-only JSONL) + a sibling `meta.json` sidecar. Session id = the
  `session_<…>` dir (`SessionIDFromPath`, since the filename is the constant
  `messages.jsonl`).
- **Parser** — `user → user_message`; `assistant` + `tool_calls → assistant_message`
  (working); text-only `assistant → turn_done`; `tool → tool_result` (linked by
  `tool_call_id`).
- **Sidecar** — supplies cwd (`environment.working_directory`), model name
  (`config.active_model`), and context tokens (`stats.context_tokens`), which the
  JSONL itself lacks.
- **Process** — `CommandPattern` (vibe is a Python console-script; comm is the
  interpreter). PID bound by cwd.

Verification: `go vet`, full `go test ./core/... -race`, `go test
./tools/onboarding-factory/...`, `tools/replay-fixtures.sh`, `of validate` — all
green. End-to-end: replaying the real 336-event transcript through the production
tailer yields a clean `working → ready` arc, 0 flickers.

### Corrections to the handover doc

1. Tool calls use the OpenAI **nested** shape `tool_calls[].function.name`, not
   the flat `tool_calls[].name` the doc claimed.
2. `vibe` is a Python script with no `setproctitle`, so an `ExactName "vibe"`
   process match never fires — it needs a `CommandPattern` on the command line.

## Assess sweep — full matrix (46 cells)

Rollup: **1** recordable now · **23** driver-gap (seam to port) · **8**
daemon-bug (feasible unlock) · **7** unobservable (frozen) · **7** n.a. (frozen).

| # | Scenario | agent | daemon | driver | route |
|---|---|---|---|---|---|
| 1.1 | session-start | yes | full | gap:wait_turn | driver-gap |
| 1.2 | session-end | yes | full | gap:sigkill | driver-gap |
| 1.3 | long-idle-live-session | yes | full | gap:wait_turn | driver-gap |
| 1.4 | session-resume | yes | full | gap:resume | driver-gap |
| 1.5 | session-reset | yes | full | gap:wait_turn | driver-gap |
| 1.6 | checkpoint-rewind | yes | full | gap:keys | driver-gap |
| 1.7 | cloud-background-agent | yes | incapable | ready | **frozen** |
| 1.8 | model-context-display | yes | **bug** | gap:wait_turn | driver-gap |
| 2.1 | basic-turn | yes | full | gap:wait_turn | driver-gap |
| 2.2 | auto-executed-tool-call | yes | full | gap:wait_turn | driver-gap |
| 2.3 | task-list | yes | **bug** | gap:wait_turn | driver-gap |
| 2.4 | self-correction-iteration | yes | full | gap:wait_turn | driver-gap |
| 2.5 | synchronous-slash-command | yes | full | gap:wait_turn | driver-gap |
| 2.6 | long-agentic-session-stress | yes | full | gap:wait_turn | driver-gap |
| 2.7 | **autonomous-loop** | partial | full | ready | **record** |
| 2.8 | autonomous-loop-iteration-limit | no | n/a | ready | **frozen** |
| 2.9 | token-quota-exhausted | yes | incapable | — | **frozen** |
| 2.10 | mid-turn-message-queued | yes | full | gap:wait_turn | driver-gap |
| 2.11 | auto-classified-permission | yes | full | gap:wait_turn | driver-gap |
| 2.12 | context-compaction | yes | full | gap:wait_turn | driver-gap |
| 2.13 | turn-end-terminal-text | yes | full | gap:wait_turn | driver-gap |
| 2.14 | turn-aborted-by-error | yes | incapable | ready | **frozen** |
| 2.15 | shell-escape-command | yes | **bug** | gap:wait_turn | driver-gap |
| 2.16 | oversized-transcript-line | yes | full | gap:wait_turn | driver-gap |
| 2.17 | user-blocking-question | yes | full | gap:wait_turn | driver-gap |
| 2.18 | user-blocking-plan-mode-approval | yes | incapable | ready | **frozen** |
| 2.19 | tool-gate-permission-prompt | yes | incapable | ready | **frozen** |
| 2.20 | user-esc-interrupt | yes | incapable | gap:interrupt | **frozen** |
| 2.21 | streaming-partial-writes | yes | full | gap:wait_turn | driver-gap |
| 3.1 | foreground-subagent | yes | **bug** | gap:wait_turn | driver-gap |
| 3.2 | background-subagent | no | n/a | n/a | **frozen** |
| 3.3 | background-process | no | n/a | n/a | **frozen** |
| 3.4 | subagent-orphan-cleanup | yes | **bug** | gap:sigkill | driver-gap |
| 3.5 | workflow-fanout | no | n/a | ready | **frozen** |
| 4.1 | multiple-sessions-same-cwd | yes | full | gap:start_session | driver-gap |
| 4.2 | multiple-agents-same-workspace | yes | full | gap:wait_turn | driver-gap |
| 5.1 | token-accounting | yes | **bug** | gap:wait_turn | driver-gap |
| 5.2 | model-identification | yes | full | gap:wait_turn | driver-gap |
| 5.3 | model-switch-midsession | yes | full | gap:keys | driver-gap |
| 5.4 | architect-editor-pair | yes | incapable | ready | **frozen** |
| 5.5 | provider-failover-midturn | no | incapable | ready | **frozen** |
| 5.6 | subscription-detection | no | n/a | ready | **frozen** |
| 5.7 | quota-burndown | no | n/a | ready | **frozen** |
| 5.8 | task-estimate-marker | yes | full | gap:wait_turn | driver-gap |
| 6.1 | backchannel-control | yes | **bug** | ready | record-known-failing |
| 6.2 | backchannel-observe | yes | **bug** | ready | record-known-failing |

## Feasible daemon-bug unlocks (an unlock pass would build these)

All eight `daemon=bug` cells are fixable — each cited to source:

- **2.15 shell-escape** — the `injected:true` "Manual `!` command result … context
  only" user line wrongly registers as activity → session sticks in `working`.
  Fix: skip that line in the vibe parser. *(A regression introduced by the new
  adapter — highest priority.)*
- **3.1 foreground-subagent + 3.4 subagent-orphan-cleanup** — vibe's Task tool
  writes a child dir `<parent>/agents/<child>/` with `parent_session_id`, but the
  adapter drops the link. Fix: wire `ParentSessionID` + a `deriveParentSession`
  rule.
- **2.3 task-list** — vibe ships a `todo` tool the parser ignores. Fix: decode it
  to `TaskDeltas` (ref opencode's `todowrite`).
- **5.1 token-accounting** — cost + cumulative tokens live in `meta.json.stats`
  but aren't emitted. Fix: emit per-turn `Contribution` on live-tail.
- **1.8 model-context-display** — model name displays; the context-window bar
  needs mistral models in `core/pkg/capacity` (separate package).
- **6.1 backchannel-control + 6.2 backchannel-observe** — declare
  `Control{SupportsInput:true, Interrupt:InterruptCtrlC}` + `ControlPermission`
  (ref kirocli), verified via `contracttesting.AssertPermissionGated`; 6.2 also
  needs vibe `DetectUI` markers.

## Honest freezes (not gaps to chase)

- **Error/abort/ESC** (2.9, 2.14, 2.20) — vibe persists no terminal line on
  these (TUI-only) → the session sticks in `working`. Shared root cause; a daemon
  working-state idle sweep would help (bigger shared change).
- **Waiting-pause emission** (2.18 plan-mode, 2.19 tool-gate, 5.4 architect) —
  vibe *has* these features, but the `working → waiting` pause is never flushed
  to `messages.jsonl` (per-turn flush happens after the blocking call).
- **Cloud / architecture** (1.7 `/teleport` → Mistral-hosted, no local file;
  3.2/3.3/3.5 in-process synchronous subagents + synchronous bash + no
  orchestration; 5.5/5.6/5.7 no failover + PAYG API-key, no subscription/quota).

## Next steps

1. **Unlock pass** on the 8 `daemon=bug` cells (start with 2.15 — the introduced
   regression), each with unit tests + a live check.
2. **Record** the recordable cells — 2.7 now; the driver-gap cells after `record`
   ports the driver seams (launch binary `vibe`, `wait_turn` turn-done poll,
   transcript-path capture). Prereqs: `vibe` on PATH + `MISTRAL_API_KEY`.
