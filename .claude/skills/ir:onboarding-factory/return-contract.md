# Subagent return contract (shared)

Every onboarding-factory subagent (`create-scenario`, `create-agent`, `assess`,
`record`) returns ONE short block to the dispatcher — nothing else. The
dispatcher collects these into a table and never re-reads the per-cell tool
output, so the block is the whole interface. This doc defines the SHARED rules
and the cross-cutting fields once; each verb's own `## Return contract` section
lists its exact field set and points here for shared semantics.

## Envelope rules (all verbs)

- **Return ONLY the block.** No transcripts, no tool logs, no narration.
- **Stay within the line budget** stated in the verb's section (≤6 lines).
- **Never ask the dispatcher a question.** A subagent runs to a terminal
  outcome and reports it. If blocked, return the blocking status with the
  concrete blocker named in `notes` — not a question.
- **First line is the outcome key**: a `status:` / `route:` / `verdict:` /
  `scenario_id:` line the dispatcher keys on to decide the next step.
- **The factory is the only writer.** Every mutation a verb makes is an `of`
  call. A subagent never edits `replaydata/` by hand.

## Cross-cutting fields (defined once, used by more than one verb)

- **`commit_sha`** — short SHA of the commit this verb made (scenario row,
  agent column, cell assessment+spec, or recording). `n/a` when it committed
  nothing.
- **`notes`** — one or two sentences: the load-bearing reason, a drift flag, a
  scope note, a retry count, or an infra reason. Prose, not data.
- **`prereq_blocked`** — a `status:` value (`assess`, `record`) for a cell that
  can't proceed because of a human action the subagent can't take (switch to an
  API key, install/auth a CLI, provide a mock). Name the exact blocker in
  `notes`; the dispatcher surfaces it to the human. Distinct from a pillar
  verdict (the agent/daemon/driver genuinely can't) — this is an environment
  gap, orthogonal to the three pillars.
- **`observability_correction`** (`record`) — set when the LIVE recording
  disagrees with the doc-based `assess` verdict (e.g. assessed
  `daemon=full` but the transcript/store proved the signal isn't emitted). A
  non-`none` value MUST be written back into the cell via `of cell write` in
  the SAME commit (correct the pillar, add a citing caveat) — the backflow
  loop, not a cue. `none` when the recording matched the assessment.

## The three pillars (the assessment axes)

Every cell carries a three-pillar verdict — these mean the same wherever they
appear:

- **agent** (`agent_supports`) — `yes` | `partial` | `no` | `unknown`. Does the
  agent CLI perform the behavior at all?
- **daemon** (`daemon_capability`) — `full` | `bug` | `incapable` | `n/a` |
  `unknown`. Given a recording + a working driver, can irrlichd observe it?
  `bug` = a trace exists but the daemon mis-handles it (file an issue, record
  `known_failing`); `incapable` = no trace exists / the 3-state model can't
  represent it.
- **driver** (`driver_capability`) — `ready` | `gap:<primitive>`. Does the
  agent's interactive driver implement every step type the recipe needs?

Route is derived from the three (the dispatcher reads it off `of status`):

| agent | daemon | driver | route | meaning |
|---|---|---|---|---|
| yes/partial | full | ready | **record** | proceed to `record` |
| yes/partial | bug | ready | **record-known-failing** | record + file an issue; spec `known_failing` |
| yes/partial | full/bug | gap:* | **driver-gap** | `record` ports the step, then drives |
| yes/partial | incapable | any | **frozen** | irrlicht fundamentally can't observe — no recording |
| no | n/a | n/a | **frozen** | agent lacks the feature |
| unknown | any | any | **frozen** | inconclusive — re-assess after the named gap closes |

## Per-verb field sets

The canonical field list lives in each verb's own `## Return contract`:

| Verb | First-line key | Verb-specific fields |
|---|---|---|
| `create-scenario` | `scenario_id:` | `wrote`, `acceptance`, `next` |
| `create-agent` | `agent:` | `provider`, `prereqs`, `driver_needs`, `next` |
| `assess` | `verdict:` + `route:` | `summary`, `wrote`, `prereqs` |
| `record` | `status:` | `commit_sha`, `pass_rate`, `observations`, `observability_correction` |
