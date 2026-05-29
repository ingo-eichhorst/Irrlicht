# Scenario Meanings

Canonical semantic spec for every scenario in the catalog. Each section is keyed by scenario ID (the kebab slug used in `scenarios.json` and `replaydata/`).

**Recipe and assess skills:** read this file at Step 1 to get the "What this means" block before reading the Scenario/Expected text from `.specs/agent-scenarios.md` (or the `/api/catalog` fallback). If a scenario ID is missing from this file, stop and ask the maintainer to add it.

Five fields per scenario:
- **Essence** — one sentence: what user action or system event is being exercised.
- **User-observable signal** — what appears in the dashboard proving this happened.
- **Primitive exercised** — the capability this depends on; maps to a `capabilities.json` key. Name the specific API surface.
- **Not to be confused with** — adjacent scenarios and the distinction.
- **Conceptual flow** — ordered bullets at user/agent/dashboard level. No event-kind names, no tmux commands.

---

### session-start

- **Essence:** The daemon discovers a newly launched agent process and surfaces a visible session row before the agent does anything.
- **User-observable signal:** Session row appears in `ready` within 1 second of agent launch; PID is bound.
- **Primitive exercised:** Process discovery via PID scan + first transcript write detection. Requires `headless_mode`.
- **Not to be confused with:** `session-resume` — an existing `session_id` reappears under a new PID; `session-reset` — a new session follows an intentional in-app reset.
- **Conceptual flow:**
  1. User launches the agent in a fresh directory.
  2. Daemon's PID scanner picks up the new process.
  3. Daemon tails the agent's transcript file.
  4. Session row appears in the dashboard in state `ready`.
  5. PID is bound to the session.

---

### session-end

- **Essence:** The daemon correctly removes a session after the agent exits cleanly, is killed, or crashes mid-turn.
- **User-observable signal:** Session row disappears within the expected TTL; state immediately before disappearing is `ready`.
- **Primitive exercised:** Process-exit detection (SIGKILL, clean exit, crash) plus idle-TTL expiry. No special agent capability beyond `headless_mode`.
- **Not to be confused with:** `session-resume` — a killed session reappears with the same `session_id`; `session-reset` — user triggers a new session inside the running agent.
- **Conceptual flow:**
  1. Agent exits, is killed, or crashes.
  2. Daemon detects process exit within one sweep interval.
  3. If state was `working`, it resolves to `ready`.
  4. Session row disappears within `readyTTL`.

---

### long-idle-live-session

- **Essence:** A session whose agent process is still alive is NOT pruned even after `readyTTL`, and resumes normally on the next prompt.
- **User-observable signal:** Session row persists in `ready` past `readyTTL`; `ready → working` resumes correctly on the next user prompt.
- **Primitive exercised:** Process-liveness check — daemon polls the PID to distinguish "idle but alive" from "dead and eligible for pruning". Requires `headless_mode`.
- **Not to be confused with:** `session-end` via SIGKILL-mid-idle — process is actually dead; `token-quota-exhausted` — session is alive but agent can't produce new turns.
- **Conceptual flow:**
  1. Agent finishes a turn and enters `ready`.
  2. User sends no prompts for an extended period (past `readyTTL`).
  3. Daemon checks the PID — process is still alive.
  4. Session row remains visible in `ready`.
  5. User eventually sends a prompt; `ready → working` resumes normally.

---

### session-resume

- **Essence:** When an agent is re-launched pointing at a prior session, irrlicht presents a single continuous session row across multiple PID lifetimes.
- **User-observable signal:** Same `session_id` row reappears after relaunch; state and history are preserved; each new PID lifetime drives its own state transitions correctly.
- **Primitive exercised:** `session_id_assignment` — agent uses a stable session ID across PID lifetimes (via `--resume` flag or equivalent).
- **Not to be confused with:** `session-start` — fresh session with a brand-new `session_id`; `session-reset` — intentional new-ID restart inside the running agent.
- **Conceptual flow:**
  1. Agent runs, does work, and exits cleanly.
  2. User re-launches the agent with a resume flag pointing at the prior session.
  3. Daemon sees the same `session_id` become active under a new PID.
  4. Dashboard shows the original session row — not a new one.
  5. New turns proceed; each lifetime's arc is independently correct.

---

### session-reset

- **Essence:** User deliberately abandons the current conversation inside the running agent and gets a clean slate with a new session ID.
- **User-observable signal:** Old session row disappears; a new session row appears with a different `session_id` in `ready`.
- **Primitive exercised:** `slash_commands` — agent's reset command (`/clear`, `/new`, `/reset`) ends the current session in-process and starts a fresh one without relaunching the binary.
- **Not to be confused with:** Quitting and relaunching (which looks similar but is `session-end` + `session-start`); `checkpoint-rewind` — rewinds history but keeps the same `session_id`.
- **Conceptual flow:**
  1. User is in an active session.
  2. User issues the agent's reset command.
  3. Old session row disappears.
  4. New session row appears with a new `session_id` in `ready`.

---

### checkpoint-rewind

- **Essence:** User rolls back files and/or conversation state to an earlier checkpoint; irrlicht reflects the restored state correctly and subsequent turns proceed from the rewound point.
- **User-observable signal:** Session row shows state `ready` after rewind; same `session_id` if rewound in place, or a linked new `session_id` if the adapter forks; turns after the rewind follow a clean arc.
- **Primitive exercised:** Checkpoint/branch capability (adapter-specific: Cline shadow-git rewind, Plandex plan branches, Replit checkpoint, Cursor rewind). Maps to `checkpoint_rewind` in `capabilities.json` when present.
- **Not to be confused with:** `session-reset` — discards history and starts fresh; `session-resume` — resumes after exit rather than rewinding within a running session.
- **Conceptual flow:**
  1. Agent is in `ready` after some turns.
  2. User triggers the adapter's rewind to an earlier checkpoint.
  3. Files and/or conversation state are restored.
  4. Session row reflects state `ready`.
  5. Next user prompt starts cleanly from the rewound point: `ready → working → ready`.

---

### cloud-background-agent

- **Essence:** Agent runs in a cloud VM or container with no local PID; irrlicht tracks it via an API feed or sidecar rather than process scanning.
- **User-observable signal:** Session appears with PID = 0 or absent; state transitions mirror the remote agent's progress; session disappears when the remote run completes or errors.
- **Primitive exercised:** Remote-process transport (API feed, sidecar, webhook). Maps to `cloud_agent` in `capabilities.json` when present. Requires an adapter that does NOT rely on `FilesUnderRoot` or `FilesUnderCWD`.
- **Not to be confused with:** `background-subagent` — runs a local agent session detached from the parent; `session-start` — relies on local PID discovery.
- **Conceptual flow:**
  1. User triggers a cloud/remote agent run.
  2. Daemon connects to the remote feed.
  3. Session appears in the dashboard with PID = 0 or absent.
  4. State transitions mirror the remote agent's progress.
  5. Session disappears when the remote run completes, errors, or is dismissed.

---

### basic-turn

- **Essence:** The simplest possible turn — one user message, one assistant reply, no tool use — validates that the parser correctly identifies a turn boundary.
- **User-observable signal:** `ready → working → ready`; `TotalTokens > 0` after the turn.
- **Primitive exercised:** Transcript turn-end detection — parser identifies the end of an assistant message as a complete turn boundary. Requires `headless_mode`.
- **Not to be confused with:** `auto-executed-tool-call` — same state arc but with tool calls; `synchronous-slash-command` — no LLM call at all; `streaming-partial-writes` — focuses on partial flush behavior.
- **Conceptual flow:**
  1. Agent is in `ready`.
  2. User sends a simple prompt.
  3. State transitions `ready → working`.
  4. Agent emits one assistant reply and the turn-end signal.
  5. State returns to `ready`. Token count is non-zero.

---

### auto-executed-tool-call

- **Essence:** Agent uses tools mid-turn but completes without ever blocking on user input — state never enters `waiting`.
- **User-observable signal:** `ready → working → ready`; state never entered `waiting`.
- **Primitive exercised:** `tool_calls` — agent can invoke tools and resume autonomously without user approval for any of them.
- **Not to be confused with:** `user-blocking-question` — a tool triggers a user question; `tool-gate-permission-prompt` — a tool requires explicit permission; `auto-classified-permission` — some but not all tools auto-approve.
- **Conceptual flow:**
  1. Agent is in `ready`.
  2. User sends a prompt requiring tool use.
  3. State transitions `ready → working`.
  4. Agent calls one or more tools; all auto-resolve without user input.
  5. Agent emits the turn-end signal; state returns to `ready`.

---

### task-list

- **Essence:** Agent maintains a todo/task list within its own session — creating items and walking each through `pending → in_progress → completed` — without spawning any child session.
- **User-observable signal:** The list surfaces in the session API's `tasks` field: each reported item appears with a matching subject and its current status (`pending` → `in_progress` → `completed`). `SubagentCount` stays 0, no child sessions appear, and state follows a normal `ready → working → ready` arc.
- **Primitive exercised:** Task-list management internal to the agent's own session — Claude Code's `TaskCreate` / `TaskUpdate` tools, OpenCode's `todowrite` (which replaces the whole list on every call). Despite the "Task tool" name in Claude Code, this is unrelated to the Agent tool that spawns child sessions.
- **Not to be confused with:** `foreground-subagent` / `background-subagent` — the Agent tool spawns real child sessions with their own `session_id` and bumps `SubagentCount`; a task list does neither.
- **Conceptual flow:**
  1. Agent is in `ready`.
  2. User asks the agent to plan and track work as a list.
  3. State transitions `ready → working`.
  4. Agent creates several items, then marks each `in_progress` and `completed` via its task-list tool — no child sessions appear.
  5. The `tasks` field reflects the items and their final status; `SubagentCount` remains 0.
  6. State transitions `working → ready`.

---

### self-correction-iteration

- **Essence:** Agent fails a tool call, interprets the failure, and retries with corrected input — multiple cycles within one turn — with no state flicker between retries.
- **User-observable signal:** State stays continuously `working` across all retry cycles; no flicker to `ready` or `waiting` between attempts; final state is `ready` (or `waiting` if the agent gives up and asks).
- **Primitive exercised:** `tool_calls` + iterative retry within a single turn. The key irrlicht primitive is the classifier holding the session in `working` across multi-step retry chains that span multiple tool-call/result pairs without an intervening turn boundary.
- **Not to be confused with:** `long-agentic-session-stress` — many separate turns driven by user prompts; `turn-aborted-by-error` — agent gives up and provider error closes the turn.
- **Conceptual flow:**
  1. Agent is in `ready`.
  2. User asks the agent to run tests, build, or lint.
  3. State transitions `ready → working`.
  4. Agent runs the tool; result is a failure.
  5. Agent retries with corrected input — state stays `working`, no flicker.
  6. Retry cycles repeat until success or give-up.
  7. State returns to `ready` (or `waiting` if the agent asks the user).

---

### synchronous-slash-command

- **Essence:** User invokes a slash command that responds locally without calling the model; the session stays `ready` throughout with no `→ working` transition.
- **User-observable signal:** State stays `ready`; no `→ working` transition recorded for the slash command itself.
- **Primitive exercised:** `slash_commands` — agent has in-process commands (`/help`, `/cost`, `/status`, `/model`) that respond without an LLM round-trip.
- **Not to be confused with:** `context-compaction` — `/compact` DOES call the model; `session-reset` — `/clear` or `/new` ends the session; `basic-turn` — always calls the model.
- **Conceptual flow:**
  1. Agent is in `ready`.
  2. User invokes a no-LLM slash command (e.g. `/help`, `/cost`).
  3. Agent responds locally with no model call.
  4. State stays `ready` throughout.

---

### long-agentic-session-stress

- **Essence:** A realistic multi-turn session with complex tool use validates that state tracking stays coherent over a large transcript volume — no flicker, no stuck states.
- **User-observable signal:** Within each turn, state stays continuously `working`; between turns, clean `working → ready` and `ready → working` transitions; state never stuck.
- **Primitive exercised:** `tool_calls` + `headless_mode`. Stress-tests the transcript parser's ability to stay coherent across a large volume of interleaved assistant messages and tool-call/result pairs.
- **Not to be confused with:** `autonomous-loop` — no user prompts between iterations; `self-correction-iteration` — retry cycles within one turn, not across many turns.
- **Conceptual flow:**
  1. Agent starts in `ready`.
  2. User sends a complex prompt; state transitions `ready → working`.
  3. Agent calls multiple tools, emits multiple assistant fragments — state stays `working`.
  4. Turn ends; state transitions `working → ready`.
  5. Repeat for many turns.
  6. After the final turn: state reaches `ready` and stays there.

---

### autonomous-loop

- **Essence:** Agent drives itself through an entire task goal with no user prompts between internal iterations; irrlicht keeps the session in `working` for the whole autonomous run.
- **User-observable signal:** `ready → working` at loop start; state stays `working` for the entire run with no `ready` between internal iterations; `working → ready` when the goal is reached.
- **Primitive exercised:** Autonomous-mode / self-prompting capability (Codex `/goal`, Plandex full-auto, SWE-agent, Goose recipe). Maps to `autonomous_mode` in `capabilities.json`. The key irrlicht primitive is keeping the session in `working` across internally-generated turn boundaries where no user prompt separates iterations.
- **Not to be confused with:** `autonomous-loop-iteration-limit` — same primitive but stops at a cap before completion; `long-agentic-session-stress` — user-driven turns, no self-prompting.
- **Conceptual flow:**
  1. User starts the agent in autonomous mode (e.g. `/goal <condition>`).
  2. State transitions `ready → working`.
  3. Agent generates and executes its own next steps with no user prompts.
  4. State stays `working` throughout all internal iterations.
  5. Agent reaches the stated goal; state transitions `working → ready`.

---

### autonomous-loop-iteration-limit

- **Essence:** Autonomous-mode agent hits its max-iteration cap before completing; irrlicht resolves to `ready` cleanly rather than getting stuck.
- **User-observable signal:** State reaches `ready` even though the agent did not fully succeed; does not stick in `working`.
- **Primitive exercised:** Iteration cap enforcement — the agent CLI has a configurable max-step limit and emits a clean turn-end signal when it hits the cap. Maps to `autonomous_mode` in `capabilities.json`.
- **Not to be confused with:** `token-quota-exhausted` — a cost/token budget limit, not an iteration count; `turn-aborted-by-error` — an unplanned provider error rather than a planned cap.
- **Conceptual flow:**
  1. Agent is running in autonomous mode; state is `working`.
  2. Agent reaches its configured max-iteration count.
  3. Agent emits a terminal turn signal and stops self-prompting.
  4. State transitions `working → ready`.

---

### token-quota-exhausted

- **Essence:** Agent hits a hard token or subscription cost cap mid-turn; session resolves cleanly to `ready` and does not re-enter `working` until the budget resets.
- **User-observable signal:** State reaches `ready`; subsequent user prompts stay `ready` until the budget resets.
- **Primitive exercised:** Budget enforcement — the agent CLI enforces a token or time budget and emits a turn-end signal when the cap is hit. Maps to the adapter's standard turn-end path.
- **Not to be confused with:** `autonomous-loop-iteration-limit` — iteration count cap, not a cost/token cap; `turn-aborted-by-error` — unexpected provider error, not a self-imposed budget.
- **Conceptual flow:**
  1. Agent is `working` through a long turn.
  2. Agent hits its token or cost budget.
  3. Agent emits a turn-end signal and stops.
  4. State transitions `working → ready`.
  5. Further user prompts keep the session in `ready` until budget resets.

---

### mid-turn-message-queued

- **Essence:** User sends a follow-up while the agent is mid-turn; the agent runtime queues it and keeps processing, with no state flicker on arrival.
- **User-observable signal:** State stays `working` when the queued message arrives; no `→ ready` flicker; cycle eventually ends at `ready`.
- **Primitive exercised:** Message-queue handling — the agent's runtime buffers a user message during an active turn without emitting a turn boundary. The irrlicht primitive is that the transcript does NOT emit a turn boundary when the queued message arrives.
- **Not to be confused with:** `user-blocking-question` — AGENT pauses for user input, not user sending unsolicited mid-turn input; `streaming-partial-writes` — about partial assistant content, not queued user input.
- **Conceptual flow:**
  1. Agent is `working` through a long turn.
  2. User types a follow-up message.
  3. Agent runtime queues it; the transcript shows no turn boundary.
  4. State stays `working` throughout.
  5. Agent finishes the current turn; picks up the queued message; cycle ends at `ready`.

---

### auto-classified-permission

- **Essence:** An internal classifier auto-approves safe tool calls but gates risky ones, producing a `waiting` state only for risky ones.
- **User-observable signal:** Safe calls: `ready → working → ready`, never entering `waiting`; risky call: `working → waiting`; after user approval, `waiting → working → ready`.
- **Primitive exercised:** `permission_hooks` — agent's auto-permission classifier distinguishes safe from risky tool calls and selectively prompts the user only for risky ones.
- **Not to be confused with:** `tool-gate-permission-prompt` — EVERY tool call requires explicit approval; `auto-executed-tool-call` — ALL calls auto-approve with no gating.
- **Conceptual flow:**
  1. Agent is in `ready`.
  2. User sends a prompt involving both safe edits and one risky shell command.
  3. Safe tool calls execute; state stays `working`, never entering `waiting`.
  4. Risky tool call: state transitions `working → waiting`.
  5. User approves; state transitions `waiting → working → ready`.

---

### context-compaction

- **Essence:** Context window fills or user invokes compaction; the agent summarizes the conversation in place (via an LLM call) and irrlicht keeps the session in `working` throughout.
- **User-observable signal:** `ready → working` at compaction start; state stays `working` throughout; `working → ready` when done; no further transitions until the next user prompt.
- **Primitive exercised:** `context_curation` / compaction command (e.g. Claude Code `/compact`). Agent makes an LLM call to summarize context and emits a turn-end signal when complete.
- **Not to be confused with:** `synchronous-slash-command` — uses no LLM; `basic-turn` — normal user-driven turn with no compaction; `autonomous-loop` — self-prompting is not compaction.
- **Conceptual flow:**
  1. Agent is in `ready`.
  2. User invokes `/compact` or auto-compaction triggers.
  3. State transitions `ready → working`.
  4. Agent makes an LLM summarization call; state stays `working`.
  5. Agent emits the turn-end signal; state transitions `working → ready`.

---

### turn-end-terminal-text

- **Essence:** The parser correctly detects adapter-specific end-of-turn signals and resolves the session to `ready` regardless of which adapter's terminal token ends the turn.
- **User-observable signal:** `ready → working → ready`; state reaches `ready` regardless of which adapter-specific signal closes the turn.
- **Primitive exercised:** Turn-end parsing — adapter emits or parser detects the correct end-of-turn token (`stop_reason: end_turn`, `task_complete`, `stopReason: stop`, etc.).
- **Not to be confused with:** `turn-aborted-by-error` — no normal end signal arrives; `streaming-partial-writes` — focuses on partial flushes before the final end signal; `basic-turn` — the claudecode baseline.
- **Conceptual flow:**
  1. Agent is in `ready`.
  2. User sends a prompt; state transitions `ready → working`.
  3. Agent emits the adapter-specific turn-end signal.
  4. Parser recognizes the signal; state returns to `ready`.

---

### turn-aborted-by-error

- **Essence:** The model call fails mid-turn with no turn-end signal; the daemon's sweep detects the stall and resolves the session to `ready`.
- **User-observable signal:** State reaches `ready` within one sweep interval after the failure; does not stick in `working`.
- **Primitive exercised:** Sweep-based stall detection — daemon's periodic sweep identifies sessions stuck in `working` after a process event or timeout and forces a `→ ready` transition.
- **Not to be confused with:** `autonomous-loop-iteration-limit` — planned cap exit; `token-quota-exhausted` — self-imposed budget exit; `user-esc-interrupt` — user-driven cancel.
- **Conceptual flow:**
  1. Agent is `working` through a turn.
  2. Provider call fails (network error, 5xx, timeout); no turn-end signal is emitted.
  3. Agent gives up.
  4. Daemon's sweep detects the session stuck in `working`.
  5. State resolves to `ready` within the sweep interval.

---

### shell-escape-command

- **Essence:** Agent runs a shell command outside the formal tool framework; irrlicht stays `working` during execution and resolves to `ready` when the command exits.
- **User-observable signal:** `ready → working → ready`; state does not stick in `working` after the shell command exits.
- **Primitive exercised:** Shell-escape mechanism (Claude Code `!cmd`, or analogous). The irrlicht primitive is that the parser handles shell-escape output (distinct from a formal tool-call/result pair) and still resolves to `ready`.
- **Not to be confused with:** `auto-executed-tool-call` — uses the formal tool framework; `background-process` — a detached process that keeps the session `working` past the turn.
- **Conceptual flow:**
  1. Agent is in `ready`.
  2. User sends a prompt containing a shell escape (e.g. `!ls`).
  3. State transitions `ready → working`.
  4. Shell command runs and exits.
  5. State returns to `ready`.

---

### oversized-transcript-line

- **Essence:** A single transcript event is unusually large; the parser handles it without stalling and the session resolves to `ready` normally.
- **User-observable signal:** `ready → working → ready`; no parser stall causing stuck `working`.
- **Primitive exercised:** Parser robustness — transcript tailer and line parser correctly handle lines exceeding normal size (>2 MB). No special agent capability required.
- **Not to be confused with:** `long-agentic-session-stress` — many turns with large total volume, not a single oversized line; `streaming-partial-writes` — partial assistant content flushes.
- **Conceptual flow:**
  1. Agent is in `ready`.
  2. User sends a prompt triggering very large tool output (e.g. a base64 artifact or long diff).
  3. State transitions `ready → working`.
  4. The oversized transcript line is buffered and parsed without stalling.
  5. State returns to `ready`.

---

### user-blocking-question

- **Essence:** Agent pauses mid-turn to ask the user a question via a dedicated question tool; the dashboard enters `waiting` and stays there until the user replies.
- **User-observable signal:** `ready → working → waiting`; after user replies, `waiting → working → ready`; the `waiting` episode is visible (no skip-over).
- **Primitive exercised:** `tool_calls` + AskUserQuestion (or adapter equivalent) — agent can pause mid-turn and await structured user input via a dedicated question tool call.
- **Not to be confused with:** `tool-gate-permission-prompt` — a tool requires explicit permission, not a question; `user-blocking-plan-mode-approval` — pause is for plan review; `mid-turn-message-queued` — USER sends unsolicited input, agent doesn't request it.
- **Conceptual flow:**
  1. Agent is `working` through a turn.
  2. Agent calls AskUserQuestion and pauses.
  3. State transitions `working → waiting`.
  4. User sees the question and types a reply.
  5. State transitions `waiting → working`; agent completes the turn.
  6. State reaches `ready`.

---

### user-blocking-plan-mode-approval

- **Essence:** Agent emits a plan and halts for user approval; irrlicht shows `waiting` during the approval pause and never enters `ready` between plan emission and the user's response.
- **User-observable signal:** `ready → working → waiting`; state never entered `ready` between plan emission and user reply; after approval, `waiting → working → ready`.
- **Primitive exercised:** `plan_mode` — agent has a plan-review gate (Claude Code `ExitPlanMode`, Codex `<proposed_plan>` block) that blocks further progress until the user approves or dismisses.
- **Not to be confused with:** `user-blocking-question` — pause is an AskUserQuestion call; `tool-gate-permission-prompt` — pause is a tool permission request.
- **Conceptual flow:**
  1. Agent is `working`.
  2. Agent emits a plan and enters plan-mode pause.
  3. State transitions `working → waiting`.
  4. User reviews the plan and approves (or dismisses with a new prompt).
  5. State transitions `waiting → working → ready`.

---

### tool-gate-permission-prompt

- **Essence:** A tool call triggers an explicit permission prompt; the dashboard enters `waiting` until the user resolves it, and a late post-tool hook does NOT bounce the session back.
- **User-observable signal:** `ready → working → waiting`; after user resolves, `waiting → working → ready`; a delayed post-tool hook arriving after `→ ready` does NOT change state.
- **Primitive exercised:** `permission_hooks` — agent's tool framework has an explicit permission prompt that blocks execution until the user approves or denies.
- **Not to be confused with:** `auto-classified-permission` — safe calls auto-approve and only risky ones gate; `user-blocking-question` — pause is an AskUserQuestion call, not a permission gate.
- **Conceptual flow:**
  1. Agent is `working`.
  2. Agent attempts a tool call requiring permission; state transitions `working → waiting`.
  3. User approves or denies; state transitions `waiting → working`.
  4. Agent completes the turn; state reaches `ready`.
  5. A delayed PostToolUse hook arrives; state stays `ready`.

---

### user-esc-interrupt

- **Essence:** User cancels a running turn mid-response with the interrupt key before the agent finishes; state resolves cleanly to `ready`, and the next prompt proves the parser reset.
- **User-observable signal:** `ready → working → ready`; state does not stick in `working` post-interrupt.
- **Primitive exercised:** Interrupt-during-turn — agent CLI exposes a cancel signal (ESC for claudecode/codex/pi, Ctrl-C for aider) and resets cleanly so the next prompt drives a fresh `ready → working → ready` arc.
- **Not to be confused with:** Pressing ESC while the agent is already idle (no-op, state stays `ready`); `turn-aborted-by-error` — turn ends without user action due to a provider error.
- **Conceptual flow:**
  1. Agent is idle in `ready`.
  2. User sends a prompt that will take >2s to answer.
  3. State transitions `ready → working`.
  4. Before the agent emits a turn-done marker, user presses the interrupt key.
  5. State transitions `working → ready`.
  6. Next user prompt drives a clean `ready → working → ready` arc (proving the parser reset).

---

### streaming-partial-writes

- **Essence:** The assistant message arrives in multiple partial flushes before the turn-end signal; state stays `working` across all flushes with no premature `→ ready`.
- **User-observable signal:** `ready → working → ready`; state stays `working` across all partial flushes — no flicker to `ready` before the turn-end signal arrives.
- **Primitive exercised:** `streaming_output` — agent emits assistant content in incremental chunks before the final turn-end signal. The irrlicht primitive is that partial flushes do NOT trigger a `→ ready` transition.
- **Not to be confused with:** `basic-turn` — arrives as a single flush; `mid-turn-message-queued` — partial state is about queued user input, not streamed assistant output.
- **Conceptual flow:**
  1. Agent is in `ready`; user sends a prompt.
  2. State transitions `ready → working`.
  3. Agent emits the first partial chunk — state stays `working`.
  4. Agent emits additional chunks — no flicker.
  5. Agent emits the turn-end signal; state transitions `working → ready`.

---

### foreground-subagent

- **Essence:** Parent agent spawns one or more child agents in the foreground; parent stays `working` until all children complete; children are linked by `ParentSessionID` and counted in `SubagentCount`.
- **User-observable signal:** Child sessions appear with `ParentSessionID = parent.session_id`; `SubagentCount(parent) = N` while N children are alive; `SubagentCount` decreases as each child finishes; parent transitions `working → ready` only after the last child ends.
- **Primitive exercised:** `subagents` — agent can spawn child sessions (Agent/Task tool) that run in a forked context, are linked to the parent by `session_id`, and hold the parent in `working` until they complete.
- **Not to be confused with:** `background-subagent` — parent returns to `ready` while child continues independently; `background-process` — a shell process, not an agent session; `task-list` — the todo-list variant of task management, not subagent spawning.
- **Conceptual flow:**
  1. Parent is `working`.
  2. Parent spawns one or more foreground children via Agent/Task tool.
  3. Child sessions appear in the dashboard, linked to the parent by `ParentSessionID`.
  4. `SubagentCount(parent)` reflects the live child count.
  5. Each child completes; `SubagentCount` decreases.
  6. Parent transitions `working → ready` once the last child ends.

---

### background-subagent

- **Essence:** Parent dispatches a child via a fire-and-forget background primitive (SendMessage / `run_in_background`); the dispatch returns immediately and the parent finishes its own reply, but the child keeps running detached. The session stays `working` while the background child is alive and only returns to `ready` once it drains.
- **User-observable signal:** The parent finishes its own reply but the session stays `working` as long as the background child is alive; `SubagentCount(parent)` includes the live child; the session transitions `→ ready` only after the last background child completes.
- **Primitive exercised:** `subagents` + background dispatch (SendMessage or equivalent) — agent can spawn a child whose dispatch returns immediately without blocking the parent's reply, while the daemon holds the parent `working` on the child's liveness.
- **Not to be confused with:** `foreground-subagent` — the parent's own reply never completes until the child returns its result inline; here the parent's reply completes but the session is still held `working` by the detached child. `background-process` — a shell process with no `session_id`, surfaced via `BackgroundProcessCount` rather than `SubagentCount`.
- **Conceptual flow:**
  1. Parent is `working`.
  2. Parent dispatches a background child; the dispatch returns immediately and the parent finishes its own reply.
  3. Session stays `working` because the background child is still alive; `SubagentCount(parent)` reflects it.
  4. Background child completes; its session disappears within `readyTTL`.
  5. Once no background child (or process) remains, the session transitions `working → ready`.

---

### background-process

- **Essence:** Agent spawns a long-running shell process detached from the turn; the session stays `working` while the background process is alive and resolves only when it exits.
- **User-observable signal:** `ready → working`; `BackgroundProcessCount > 0` while any background process is alive; agent stays `working` until the last background process exits; then `→ ready`.
- **Primitive exercised:** Background process management (`run_in_background: true` or shell `&`) — agent can launch detached shell processes and the daemon tracks their liveness separately from transcript turn boundaries.
- **Not to be confused with:** `background-subagent` — an agent session with a `session_id`, not a shell process; `auto-executed-tool-call` — tools complete synchronously before the turn ends.
- **Conceptual flow:**
  1. Agent is `working`.
  2. Agent calls a tool with `run_in_background: true` (or equivalent); tool returns immediately.
  3. `BackgroundProcessCount > 0` appears in the dashboard.
  4. Background process eventually exits.
  5. Agent transitions `working → ready`.

---

### subagent-orphan-cleanup

- **Essence:** After a daemon restart, all orphaned session transcripts (no matching live PID) are correctly cleaned up; state never transitions to `working` during cleanup.
- **User-observable signal:** All orphaned sessions disappear after the restart sweep; state never transitioned to `working` during cleanup.
- **Primitive exercised:** Orphan detection — daemon's startup sweep identifies sessions whose PIDs no longer exist and removes them without re-entering `working`.
- **Not to be confused with:** `session-end` via SIGKILL — handled during normal daemon operation, not on restart; `background-subagent` ending — expected child sessions that complete normally.
- **Conceptual flow:**
  1. Multiple parent + child sessions are running.
  2. Daemon restarts.
  3. Daemon performs a startup sweep of all known session transcripts.
  4. Sessions whose PIDs no longer exist are identified as orphans.
  5. All orphaned sessions disappear; no `working` transition during cleanup.

---

### multiple-sessions-same-cwd

- **Essence:** Two agent instances sharing a working directory are tracked as independent sessions without state flapping or PID cross-contamination.
- **User-observable signal:** Two distinct session rows appear with separate `session_id`s and PIDs; each transitions state independently; no flapping between rows.
- **Primitive exercised:** PID-based session isolation — daemon correctly binds each session to its own PID even when multiple agents share the same transcript directory and filename pattern.
- **Not to be confused with:** `session-resume` — one `session_id` reuses a directory across PID lifetimes; `multiple-agents-same-workspace` — different agent types, not duplicate instances of the same agent.
- **Conceptual flow:**
  1. Two instances of the same agent are launched in the same directory.
  2. Two distinct session rows appear in the dashboard.
  3. Each session's state transitions independently.
  4. PID-to-session binding is stable and does not cross-contaminate.

---

### multiple-agents-same-workspace

- **Essence:** Different agent types running concurrently in the same project directory are each shown with the correct adapter label and fully independent state and metrics.
- **User-observable signal:** Each session appears with its correct adapter label; states transition independently; no cross-contamination of metrics, parent linkage, or subagent counts.
- **Primitive exercised:** Multi-adapter coexistence — daemon handles simultaneous sessions from different agent types without merging their metrics, state, or subagent counts.
- **Not to be confused with:** `multiple-sessions-same-cwd` — duplicate instances of the same agent; `foreground-subagent` — intentional parent-child relationships between sessions.
- **Conceptual flow:**
  1. One instance of Agent A and one instance of Agent B are launched in the same project directory.
  2. Each appears in the dashboard with its correct adapter label.
  3. Turns in Agent A do not affect Agent B's state, and vice versa.
  4. Metrics (tokens, model, subagent counts) are tracked separately per session.

---

### token-accounting

- **Essence:** Token counts accumulate correctly across turns and accurately reflect cache usage when a cached turn runs.
- **User-observable signal:** `TotalTokens > 0` after turn 1; `TotalTokens` non-decreasing after turn 2; `CacheReadTokens > 0` and fresh input tokens = 0 for a fully-cached turn.
- **Primitive exercised:** Token reporting — agent's transcript includes per-turn token counts (input, output, cache read/write) in a field the parser can extract. Requires `headless_mode` with token data in the transcript format.
- **Not to be confused with:** `model-identification` — validates model name and context window, not token counts; `token-quota-exhausted` — hitting a limit, not measuring cumulative usage.
- **Conceptual flow:**
  1. Agent runs turn 1; after turn 1: `TotalTokens > 0` in the dashboard.
  2. Agent runs turn 2; after turn 2: `TotalTokens` ≥ turn-1 total.
  3. Agent runs a turn that fits entirely in prompt cache.
  4. `CacheReadTokens > 0`; fresh input tokens for that turn = 0.

---

### quota-burndown

- **Essence:** Over a multi-turn session the agent surfaces a provider-managed rate-limit usage window whose used percentage climbs as quota is consumed; irrlicht reflects the rising usage and refreshes the reading each turn, all without the session ever hitting the cap.
- **User-observable signal:** The session's rate-limit usage percentage is non-decreasing turn-over-turn (after turn N ≥ after turn N-1), and the usage reading's sampled-at timestamp advances each turn (the snapshot is re-read, not replayed stale). The session never sticks; it follows a clean `ready → working → ready` arc per turn and never enters `waiting`.
- **Primitive exercised:** Rate-limit window reporting — the agent emits a provider-managed usage window (used percent, window size, reset time) on each turn boundary, in a field the parser can extract and the daemon stores sample-on-change. Maps to the adapter's rate-limit / quota window surface (not a cost-per-token reading).
- **Not to be confused with:** `token-quota-exhausted` — the cap is actually HIT and the session can't produce new turns until reset; quota-burndown stays strictly within the window and only watches the usage rise. `token-accounting` — cumulative input/output token counts, not a provider usage percentage. `subscription-detection` — reads the static plan tier/billing model, not the burning-down usage window.
- **Conceptual flow:**
  1. Session is `ready`; user sends a prompt; state `ready → working`.
  2. Turn completes; the agent reports a rate-limit window with used percent P1 sampled at T1; state `working → ready`.
  3. User sends a further prompt; another turn runs and completes.
  4. The agent reports the window again: used percent P2 ≥ P1, sampled at T2 > T1.
  5. Repeating across turns, the usage percentage rises (or holds steady, never resetting) and each reading carries a fresh sampled-at — without the quota cap being reached.

---

### model-identification

- **Essence:** The dashboard correctly shows the model name and context window for the model the agent was launched with — not "unknown".
- **User-observable signal:** `ModelName = <launch model>` (not `"unknown"`); `ContextWindow` matches the model's published window.
- **Primitive exercised:** Model metadata reporting — agent's transcript includes the model name and context window size in a field the parser can extract.
- **Not to be confused with:** `model-switch-midsession` — tests model changes between turns; `architect-editor-pair` — tests two simultaneous models within one turn.
- **Conceptual flow:**
  1. Agent is launched with a specific non-default model.
  2. Agent completes a turn.
  3. `ModelName` in the dashboard reflects the configured model (not `"unknown"`).
  4. `ContextWindow` matches the model's published context window size.

---

### model-switch-midsession

- **Essence:** Agent changes its model between turns; the dashboard updates `ModelName` to reflect the latest model used, and token counts continue accumulating.
- **User-observable signal:** After turn 1: `ModelName = <model A>`; after turn 2: `ModelName = <model B>`; `TotalTokens` after turn 2 > after turn 1.
- **Primitive exercised:** Per-turn model selection — agent can change models between turns (via `/model` or config) and the transcript reflects the new model in each turn's usage metadata.
- **Not to be confused with:** `model-identification` — static model throughout the session; `architect-editor-pair` — two models simultaneously within one turn; `provider-failover-midturn` — automatic failover, not user-driven switching.
- **Conceptual flow:**
  1. Agent runs turn 1 with model A.
  2. User changes the model (via `/model` or config).
  3. Agent runs turn 2 with model B.
  4. Dashboard shows `ModelName = <model B>` after turn 2.
  5. `TotalTokens` after turn 2 > after turn 1 (accumulates across model changes).

---

### architect-editor-pair

- **Essence:** A unit of work splits into an **architect/planning contribution** that produces a plan, then an **editor/execution contribution** that applies the edits; irrlicht reports the full lifecycle and accumulates token contributions across both. The pattern admits **two instantiations**:
  - **(a) Two models in one turn** (Aider `--architect` and similar dual-model adapters): a strong reasoning model plans, a cheaper editor model emits the edits, both inside one `ready → working → ready` turn.
  - **(b) Plan→implement mode-pair across the approval gate** (Claude Code plan mode): the **architect** is the *plan phase* — the agent analyzes and proposes a plan via `ExitPlanMode`, gating the session into `waiting`; the **editor** is the *implement phase* — once the user approves, the agent applies the edits and the turn completes. One model throughout.
- **User-observable signal:**
  - (a) `ModelName` reflects the architect (or the adapter's chosen primary); `TotalTokens` accumulates from both models in the same turn; clean `ready → working → ready` arc with no flicker from the model handoff.
  - (b) the full `ready → working → waiting → working → ready` arc, with `TotalTokens` accumulating across the plan and implement phases, one consistent `ModelName`, and a real edit (`Edit`/`Write`) landing in the implement phase.
- **Primitive exercised:** `multi_model_orchestration` (instantiation a) OR `plan_mode` + implement (instantiation b). The shared irrlicht primitive is reporting a planning contribution and an editing contribution as one coherent lifecycle, accumulating tokens across both.
- **Not to be confused with:** `model-switch-midsession` — user-driven model change between independent turns; `provider-failover-midturn` — automatic mid-turn failover; **`user-blocking-plan-mode-approval` (2.18)** — drives the *same* plan→`ExitPlanMode`→`waiting` entry but **terminates at the `waiting` gate** (asserts only entry into the plan-approval pause). `architect-editor-pair` instantiation (b) is distinct because it **continues past approval** through the editor/implement phase to `ready` — asserting the post-approval `waiting → working → ready` continuation, a real edit, and cross-phase token accumulation.
- **Conceptual flow (a — dual-model, one turn):**
  1. `ready`; user sends a prompt. 2. `ready → working`. 3. Architect model plans (internal to the turn). 4. Editor model emits the edits (internal to the turn). 5. Tokens accumulate from both calls. 6. `working → ready`; `ModelName` reflects the primary.
- **Conceptual flow (b — plan→implement mode-pair):**
  1. `ready`; agent is in plan mode; user sends a task. 2. `ready → working` (architect/plan phase). 3. Agent proposes a plan via `ExitPlanMode` → `working → waiting` (plan-approval gate; no intervening `ready`). 4. User approves → `waiting → working` (editor/implement phase). 5. Agent applies the edits (real `Edit`/`Write`); tokens accumulate across both phases. 6. `working → ready`; one consistent `ModelName`.

---

### provider-failover-midturn

- **Essence:** Primary model fails mid-turn; agent falls back to a secondary model and completes the turn; `ModelName` updates to the fallback and state stays continuous through the switch.
- **User-observable signal:** `ModelName` changes mid-turn without user action; state stays `working` through the failover; after turn completes, `→ ready` with the fallback model name visible.
- **Primitive exercised:** `multi_model_orchestration` with automatic failover (Aider auto-fallback, Crush, Goose). The irrlicht primitive is that `ModelName` updates to the fallback model mid-turn without state flickering to `ready`.
- **Not to be confused with:** `model-switch-midsession` — user-driven switching between turns; `turn-aborted-by-error` — no fallback, the turn fails entirely.
- **Conceptual flow:**
  1. Agent is `working` through a turn.
  2. Primary model returns an error (rate limit, 5xx, timeout).
  3. Agent switches to the secondary model automatically; `ModelName` updates.
  4. Agent completes the turn with the fallback model.
  5. State transitions `working → ready`; dashboard shows the fallback model name.
