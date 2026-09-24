---
name: ir:fleet
description: >
  Orchestrate several `ir:exec` runs over several Irrlicht issues at once, and
  verify what the agents actually did rather than what they reported. A
  dispatcher: it groups tickets by their declared file scope, states a
  concurrency limit and its reason, watches each running agent for the
  divergences the wave-1 run produced, checks every hand-back against a
  mechanical instrument, and corrects the specific step that went wrong. Three
  checkers do the deciding — `tools/lib/fleet-scope-overlap.sh`,
  `tools/lib/fleet-review-evidence.sh` and `tools/lib/ref-exists.sh` — so a
  verdict cites a command rather than a reading. Use when the user says
  "/ir:fleet", "run the fleet", "execute these issues together", or names
  several issue numbers to work at once. It produces ready PRs and merges
  nothing.
---

# Irrlicht fleet — dispatcher over `ir:exec`

**The iron rule: this skill does not re-teach `ir:exec`.** Both rules the
wave-1 fleet broke on 2026-09-20 were already written in
`.claude/skills/ir:exec/SKILL.md`, in plain imperative English, and three of
three agents broke the first one anyway. A sixth sentence in the same file is
not the fix. Observing what an agent does, and correcting it while it runs, is.

**Why a dispatcher.** Each ticket's work is one `general-purpose` Agent that
burns its own context and hands back a short report. The parent spends its
context on scheduling, verification and correction, which is the part no
subagent can do for itself.

**What verification means here.** Check the claim, not the conclusion. Every
wrong answer during the wave-1 run came from a check that confidently answered
a different question than the one asked: a wait keyed on a process name that a
sibling worktree also matched, an exit marker written to the outer shell
instead of the log, and a ref matched by substring against an object id. A
verdict in this skill names the command that produced it.

## Invocation, and what this refuses

```text
/ir:fleet <N> <N> <N>
```

Explicit issue numbers only. Refuse each of these, and say which one applies:

- A label, a milestone, or "every ready-for-agent issue". Expanding a query
  into a work-list is the easy way to dispatch far more work than intended.
- A ticket with no current `ready-for-agent` triage decision record. Report it
  and stop for that ticket. Do not infer a plan, and do not run `/ir:triage`
  unasked.
- Any request to merge. This skill produces ready PRs, exactly as `ir:exec`
  does.

No agent in the fleet files a GitHub issue. Never put "open a follow-up issue"
in a brief: that would reach the tracker without the maintainer ever seeing
the instruction that created it.

## Build the dispatch plan

Fetch each ticket's body into the scratchpad, then let the checker decide.
Scratchpad filenames carry the ticket number, because the scratchpad is
session-scoped and concurrent agents share it:

```bash
for n in <N> <N>; do
  gh issue view "$n" --repo ingo-eichhorst/Irrlicht --json body --jq .body \
    > "$SCRATCH/fleet-$n-body.md" ||
    { echo "REFUSE: could not fetch #$n"; exit 2; }
done
tools/lib/fleet-scope-overlap.sh "$SCRATCH"/fleet-*-body.md
```

The redirect creates the file before `gh` runs, so it leaves an empty file
behind on an auth expiry or a rate limit. Without that guard the checker reads
the empty file, says the ticket declares no scope, and the run dispatches a
ticket whose body was never fetched.

Read its exit status first, then the line that produced it:

| Status | Line | Action |
|---|---|---|
| 0 | `DISJOINT` | they may share a wave |
| 1 | `OVERLAP` | put those two in different waves |
| 2 | `UNDECLARED` | that ticket runs solo |
| 2 | `REFUSE` | the input could not be read; stop and fix it |

Status 2 carries both of the last two cases, so reading the status alone
collapses "this ticket declares no scope" into "I could not look at this
ticket". Those need different actions.

A ticket whose scope is `UNDECLARED` is not "probably fine". Most open tickets
have no `## 5.` section at all, so the answer is unknown rather than
reassuring, and an unknown scope is never co-dispatched.

**Concurrency.** Two when any declared scope touches `replaydata/` or `core/`,
otherwise three. The load observation behind that split lives in
`docs/ci-gates.md`, under "Several runs at once", together with the recovery
from a load-induced `TIMEOUT`. Cite it rather than retyping the figure.

Print the plan before any agent starts. Name the tickets, the directory keys
the checker compared, the verdict per pair, the limit, and the rule that
produced the limit.

## Dispatch a wave

One `general-purpose` Agent per ticket. Not `Explore`, which has no `Agent`
tool and so cannot reach `ir:exec`'s own review delegation. Do not pass
`isolation: "worktree"`; `ir:exec` creates its own worktree.

A spawned agent does not reliably start in this session's working directory,
so bind it explicitly. Compute the main checkout once and substitute it for
`<repo-root>`. `--show-toplevel` is the wrong question: run inside a worktree
it answers with that worktree, and every dispatched `ir:exec` would then nest
its own worktree inside the fleet's. Measured in
`.claude/worktrees/2022-ir-fleet` on 2026-09-20, `--show-toplevel` gave that
worktree while `--git-common-dir` gave `/Users/ingo/projects/irrlicht/.git`:

```bash
REPO_ROOT=$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")
```

`--path-format=absolute` is what keeps it absolute from the main checkout, where
`--git-common-dir` alone answers `.git` and the brief would say `Repo root: .`.
Measured in `/Users/ingo/projects/irrlicht` on 2026-09-24 (#2045).


```text
Agent(
  subagent_type: "general-purpose",
  description: "ir:exec <N>",
  prompt: "Read and execute .claude/skills/ir:exec/SKILL.md for issue <N>.
           Repo root: <repo-root>. FIRST cd there. Run every command from the
           worktree ir:exec creates, never from the main checkout.
           Follow section 4's concurrent-run rules exactly. They are written
           there; this brief does not restate them, because restating a rule
           an agent already has is what failed on 2026-09-20.
           One thing section 4 cannot know: you share this session's
           scratchpad with the rest of the fleet, so name every file
           fleet-<N>-<purpose>.
           Put the review findings themselves in your final text, and end the
           reviewer's hand-back with the line
           `review: effort=<tier> findings=<N>` so the evidence check can run.
           Say plainly if a delegated review returned nothing.
           File no GitHub issue. Do not merge.
           Return: the PR link, the gates that ran, the SKIP lines --changed
           printed, the review effort and its findings, and any step you could
           not complete."
)
```

## Watch a running agent

Each divergence the wave-1 run produced, with the check that catches it:

| Divergence | What it looks like | Check |
|---|---|---|
| backgrounded a gate, then ended the turn | the agent hands back with nothing committed and no PR | `git -C <worktree> log --oneline origin/main..HEAD` |
| waited on a shared process name | a `TIMEOUT` a sibling worktree caused | the brief keys every wait on a self-owned marker |
| ran a gate the diff cannot break | the Swift suite on a diff with no Swift file | `git -C <worktree> diff --name-only origin/main...HEAD` |
| reported a push that did not land | a branch that is not on the remote | `tools/lib/ref-exists.sh origin feat/<N>-<slug>` |

An agent that hands back five times without progressing is the signature of
the first row. Correct it rather than re-dispatching the whole ticket.

## Verify a hand-back

Write the agent's claim and the orchestrator's own check as separate lines, so
a check that never ran is visible as a missing line.

```bash
tools/lib/fleet-review-evidence.sh "$SCRATCH/fleet-<N>-handback.txt"
tools/lib/ref-exists.sh origin "feat/<N>-<slug>"
gh pr view <PR> --json isDraft,mergeable,mergeStateStatus,headRefOid
gh pr checks <PR>
```

`fleet-review-evidence.sh` exits 0 only when the hand-back states the effort
it ran at and carries either real findings or the explicit words "no
findings". Anything else is `UNPROVEN`. Treat a review that reported nothing
as unproven until that checker says otherwise: a dead review agent and a clean
review read identically in prose, which is the shape `AGENTS.md` forbids.

An absent check is not a passing check. Re-run the relevant checks against the
current head when the PR is conflicting.

Review the diff yourself, at the ticket's triage effort, when the delegated
review returned zero findings, or when the diff touches `core/`. Reviewing
#2018 that way found two confirmed defects that every green gate had missed,
one of them pinned by a passing test that never used the input that breaks it.

Each ticket ends with one of three words:

| Word | Meaning |
|---|---|
| `CONFIRMED` | every check agreed with the agent's claim |
| `CORRECTED` | a check disagreed; the line says what changed |
| `UNPROVEN` | a check could not run; never reported as clean |

## Correct

Re-dispatch the specific step, not the ticket. Send the agent the check that
disagreed and its output, and let it act. A brief that merely repeats the rule
the agent already broke is the failure this skill exists to replace.

## Resume after a rate limit

A session rate limit killed all four agents of wave 1 at once. Work survived
only because each had committed. Derive resume state from what `ir:exec`
already leaves on disk; do not keep a checkpoint file, which drifts away from
the real state:

```bash
git worktree list | grep -w "<N>"
git config --local --get "branch.feat/<N>-<slug>.irExecBase"
tools/lib/ref-exists.sh origin "feat/<N>-<slug>"
gh pr list --repo ingo-eichhorst/Irrlicht --state open --search "<N>" \
  --json number,isDraft,title
```

Name the existing worktree in the resume brief, so `ir:exec` resumes it
instead of stopping on its work-in-progress check.

## Report

Print the per-ticket verdict for every ticket, including any the fleet never
started. Hand back the PR links. State plainly which tickets carry an
`UNPROVEN` gate and why. Merge nothing, and recommend no merge.

## Anti-patterns

- **Do not fix a ticket's code yourself.** Correct the agent and let it act, so
  the work and its proof stay in one worktree.
- **Do not invoke the Workflow tool.** `AGENTS.md` forbids it unless the
  maintainer asks; this is orchestration by skill.
