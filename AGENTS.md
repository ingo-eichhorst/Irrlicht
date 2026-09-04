# Irrlicht — Development Guide

## Short Cuts

- Triage and plan an issue with `/ir:triage #<N>`. Execute the approved plan
  with `/ir:exec <N>`. The execution skill produces a ready PR and never merges.
- NEVER RUN: the Workflow tool (multi-agent orchestration) if not explicitly requested (too expensive)

## Process Rules

Worktrees share the parent repo's `.git` dir, so **`git stash` is not isolated per worktree** — it's a single shared stack. Concurrent agents stashing in different worktrees can pop each other's WIP. Use a local commit as a checkpoint instead (`git commit -m wip`, amend/reset later).
When encountering suboptimal processes or issues make improvment suggestions in you final answer to the user.

**Dismissals carry evidence.** Any claim whose function is *"you don't need to look
here"* — *already fixed in #X*, *self-heals*, *likely benign*, *non-issue* — gets the
same bar as the claim it supports: cite what you ran or read, or mark it assumed. This
binds wherever a finding is written down (issue bodies, PR descriptions, triage
comments, final answers), because a wrong dismissal is the one kind of error that stops
anyone from checking it. Marking works: in #1088 every claim explicitly labelled
"unverified" was investigated and came back not real, while that issue's one real bug
hid in an unmarked aside.

## Build Artifacts

Use `./.build` for build artifacts.

## Building the daemon

- **Release builds**: `tools/build-release.sh` reads the base version from
  `version.json` and produces signed universal binaries + the installer.
  The binary's `--version` output is the bare version (e.g. `0.3.13`) so
  release tags stay clean.
- **Dev builds**: `tools/build-dev.sh` produces a native binary at
  `core/bin/irrlichd` with a version string like `0.3.13+1f702e7.dirty`
  — base version plus short SHA plus `.dirty` if the worktree has
  uncommitted changes. `+` is semver build metadata so dev binaries never
  compare as "newer" than their base release.
- The string is computed by `tools/version.sh` (pass `--base` for the
  bare version.json value). `promote-recording.sh` captures it into
  archive and top-level `manifest.json` files so the viewer's metadata
  panel shows which dev build produced each recording.

## Key Conventions

- Go code follows hexagonal architecture: `domain/` → `ports/` → `adapters/` → `application/services/`
- Four session states only: `working`, `waiting`, `ready`, `error` — no cancelled state.
  The vocabulary is declared once in `session.CanonicalStates()`; derive from it, never retype it
- Errors are logged via `Logger` interface, not propagated with `fmt.Errorf`
- Child sessions (subagents and background agents) use `ParentSessionID` for parent-child linking
- Adapters declare `Permissions` on `agent.Agent` (with Apply/Remove effect closures); every read or modification an adapter performs must be consent-gated behind one of its declared permissions — nothing is exercised while pending or denied
- An adapter reading an agent-owned SQLite store (hermes, opencode, antigravity)
  opens it through `core/pkg/sqlitero`, never `sql.Open` directly. The DSN
  grammar decides whether "read-only" is real, and every wrong spelling reads
  identically to the right one: a bare path drops `mode=ro` entirely, an
  unescaped path can be truncated into a *different* file, and `_journal=WAL`
  is a write. All three shipped at some point; that package is where the
  evidence and the regression tests live.

## Testing

**House style for what counts as evidence** — full rationale and incident
history in [docs/testing-philosophy.md](docs/testing-philosophy.md):
- A defect test earns its place only by having been seen to fail before the
  fix existed; a green that was never red is a claim, not evidence.
  `ir:exec`'s "Prove and verify" section enforces this mechanically; it binds
  outside `ir:exec` too. Locks (tests pinning behavior that must *not* change) pass
  by construction — say which ones those are rather than presenting their
  green as red-first proof.
- Anything a change *adds* (a guard, a static architecture rule, a linter or
  tripwire, a derived count or score, a schema constraint, a migration, a
  config rewriter, a contract assertion) has no "before the fix" to run red
  — mutate the thing it protects instead and confirm the check goes red.
  Commit that mutation as a fixture rather than only describing it in a PR
  body nothing re-runs.
- A figure that documents behaviour states the command that produces it, or
  says plainly that it's an estimate and how it was arrived at — a number
  typed once and repeated by hand drifts silently away from what it measured.
- **A verification mechanism must fail loudly when it cannot run.** Absence
  of a finding and inability to look must never produce the same output —
  wherever a check greps, matches, mutates, shells out, or waits on a
  readiness signal, assert that the operation actually happened, not merely
  that it reported nothing (`ir:exec`'s "Prove and verify" section has the
  e2e form of this rule).
- A fixture that waits by sleeping hasn't observed what it waits for — poll
  the condition to a deadline and fail with the elapsed time instead. And a
  fixture must observe the SUBJECT, never a side effect the subject produces
  on its way out.
- A validator that can't parse its input checks MORE, never less — an input
  it can't read with confidence is the last place to drop checks.
- Code that emits bytes from a structural diff (a config rewriter, a
  formatting-preserving serializer, a patcher, a migrator) gets a property
  test generating random inputs and mutations, not only hand-written cases.

Before marking a ticket done, run the full suite — every layer must pass:

- Unit + e2e: `go test ./core/... -race -count=1` (includes the headless
  daemon startup smoke test — boots a real `irrlichd` on an ephemeral port
  under `t.TempDir()`, so it never touches the production daemon). Also runs
  `core/architecture_test.go` (hexagonal import-direction, statically
  enforced) and `core/architecture_hookbody_test.go` (which inbound hook
  body reads are confined to `hookjson`'s single decode).
- Architecture score (advisory, not a merge gate): `tools/ars-gate.sh` /
  `tools/preflight.sh`'s `arch` gate — flags a regression in the Agent
  Readiness Score (composite or any category) vs `origin/main`.
- Code health (advisory): CodeScene posts a "CodeScene Code Health Review"
  check on every PR automatically. For concrete file:line findings, run
  `/ir:sonarqube-report`.
- Every `contracttesting` obligation — permission gating, hook endpoints,
  hook disclosure, hook path confinement, hook version floors, unrecognized
  hook events, hook entry presence, hook receipts, managed user files, plus
  the package-local guarded-construction guards and the probe-cost doc
  comment convention — is exercised inside the same `go test ./core/...`
  above. New adapters and new permissions are covered by wiring one call
  each — and since #1740 a hooks-declaring adapter that skipped one is
  failed by a registry tripwire rather than by someone noticing; full
  obligation list, self-test harnesses, and incident history:
  [docs/testing-contracts.md](docs/testing-contracts.md).
- Factory: `go test ./tools/onboarding-factory/... -race -count=1`.
- Replay: `tools/replay-fixtures.sh` (golden drift, transition timing,
  read-boundary reconstruction, hook-channel grading, catalog census —
  gated in CI by linux.yml, run natively by `tools/preflight.sh`'s
  `replay fixtures` gate). Regenerate goldens with `UPDATE_REPLAY_GOLDENS=1
  go test ./tools/onboarding-factory/cmd/replay/... -count=1` (the
  `-count=1` matters) and commit only the touched adapter's goldens.
  replaydata integrity: `go run ./tools/onboarding-factory/cmd/of validate`
  (schema + referential integrity — a CI gate), plus `bash
  tools/onboarding-factory/scripts/smoke-test.sh` when a `web/` or
  recording-rig change is in play. Onboarding maturity + capability model:
  `replaydata/agents/adapters.json`, `of status --summary` shows claimed vs
  earned. Full write-up: [docs/replay-testing.md](docs/replay-testing.md).
- macOS app (only when touching `platforms/macos/`): `cd platforms/macos &&
  swift build && swift test --skip LauncherTestHarness --skip
  LauncherHarnessTests`, also run by `tools/preflight.sh --only swift` and
  CI's `macos-swift.yml`. Six image-snapshot suites are gated on the
  reference host only, permanently, and never in CI. Full write-up (why,
  and the pinned-scale/locale/timezone/`@AppStorage`/now environment seams
  that make the rest host-independent):
  [docs/swift-testing.md](docs/swift-testing.md).
- Web (only when touching a `web/` tree): `npm test` in that tree —
  `platforms/web/` (the dashboard) and
  `tools/onboarding-factory/internal/viewer/web/` (the onboarding viewer)
  are two independent suites, each with its own `node_modules`. Runs
  `vitest run`. **Never `npm install`** (rewrites `package-lock.json`) —
  use `npm ci`.
- State vocabulary: `tools/state-vocabulary-lint.sh` (#1804) flags any single
  line naming three-or-more-but-not-all of `session.CanonicalStates()` — the
  hand-typed enumeration that is complete when written and silently stale one
  state later (#1796 shipped four such defects). Sites that name a proper
  subset on purpose live in `tools/state-vocabulary-lint.waivers` with a
  reason, and a waiver that stops matching fails too. It reads one line at a
  time, so a partition split across lines — a `switch` with two arms and a
  `default` — is invisible to it: it complements review rather than replacing
  it. `tools/lib/state-vocabulary-lint_test.sh` carries the mutation fixtures.
- Skill files: `tools/skill-lint.sh` (`.claude/skills/**/*.md`). AGENTS.md's
  own 400-line budget (#1742 — this file is force-injected into every agent
  session via CLAUDE.md's `@AGENTS.md` import, so it never gets to just grow):
  `tools/agents-md-lint.sh`, tested by `tools/lib/agents-md-lint_test.sh`.
  POSIX shell: `tools/posix-lint.sh` (linux.yml; `dash -n` + shellcheck +
  checkbashisms). Bash: `tools/bash-lint.sh` (linux.yml; `shellcheck
  --shell=bash --severity=warning`). Sourced shell libraries:
  `tools/lib/shell-lib-errexit_test.sh` and the shared suite runner
  `tools/lib/shell-lib-suite.sh`. Workflow-guard tripwires that
  extract-and-execute a workflow step's `run:` block against a stub (the
  ARS badge job, the two other gist badge jobs, the replaydata deletion
  guard, the Swift snapshot-evidence copy step, the Swift test-harness
  source step, and "which bash a workflow step gets"):
  `tools/lib/*_test.sh`. All of the above run inside
  `tools/preflight.sh`'s `tools`/`skills`/`posix`/`bash` gates. Full
  write-up: [docs/ci-gates.md](docs/ci-gates.md).

### Local CI parity — catch failures before pushing

`tools/preflight.sh` runs every PR-gating check (test.yml + web-test.yml +
ars-gate.yml + linux.yml's replay-fixtures step natively, plus the full
Linux build+test gate via Docker under `--linux`) locally and prints a
pass/fail summary instead of stopping at the first failure — so before
opening a PR, run it once instead of round-tripping through GitHub Actions
per fix. Gates run cheapest first, in two phases; the order is load-bearing
under `--budget` (below).

```
tools/preflight.sh                # everything except the Linux Docker gate
tools/preflight.sh --linux        # + full Linux parity (slow: needs Docker)
tools/preflight.sh --only go      # just the test.yml-equivalent gates
tools/preflight.sh --only arch    # just the ARS architecture gate
tools/preflight.sh --only skills  # just the .claude/skills/**/*.md linter
tools/preflight.sh --only bash    # just the shellcheck lint over bash scripts
tools/preflight.sh --only swift   # just the macOS Swift build + test suite
tools/preflight.sh --budget 540   # bound the whole run; see docs/ci-gates.md
```

**For an automated caller (an agent), `--only` chunking is the recipe, not a
debugging convenience** — the unscoped run does not reliably fit a
foreground `Bash` call's 600s budget on this machine. Run each
`--only <group>` (see `tools/preflight.sh --help` for the current group
list; `--only linux` stays opt-in and needs Docker) as its own **foreground**
invocation — every gate still runs; chunking only changes how many
invocations it takes. **Never background the unscoped run to make it fit**:
a subagent is not woken by its own background job, so the run stalls
silently with the work committed but never pushed (`.claude/skills/ir:exec/SKILL.md`
carries the same recipe). The same shape recurs for any subagent driving an interactive
process, not just preflight — one Bash call per step, every wait a bounded
polling loop, `timeout N` on anything that can hang.

`tools/install-git-hooks.sh` (run once per clone; worktrees share the
parent repo's hooks automatically) wires the fast gates as a pre-push hook
via a copy of `tools/git-hooks/shim`, which resolves the **pushing** working
tree at run time. The hook runs `tools/preflight.sh --changed --budget 540`,
scoped to the packages and web trees the push's diff touches. Skip once with
`git push --no-verify`; run `tools/preflight.sh` manually (no `--changed`)
for the unscoped full gate. Read a push's exit status directly, never
through a pipe (`git push … | tail` reports `tail`'s status) — assert
`git status -sb` shows a tracking branch afterward instead.

**The budget is the part not to remove** (#1570): scoping alone did not make
the hook reliably fit an automated caller's 600s command budget. `--budget
<seconds>` makes the run bound itself — each gate gets whatever time is
left, a gate that outlives it is **killed and reported `TIMEOUT` by name**,
every gate behind it is reported **`NOT RUN`**, and both exit non-zero
(neither is a `SKIP`, which means "this diff cannot break it"). `PREPUSH_BUDGET`
overrides the hook's 540s (`0` = unbounded); an unflagged `tools/preflight.sh`
is unbounded and unchanged. The bounded runner is `tools/lib/gate-budget.sh`
(pure bash 3.2 — `timeout(1)` is not on a stock macOS).

Full write-up — the shim mechanics and its three consequences, the
`PIPESTATUS`-vs-`$pipestatus` trap, what the budget made visible (`gosec`
running twice per module, the `swift` gate able to consume the whole
budget), and the security gate's own double-scoping (trigger regex +
`--changed` module/tree selection): [docs/ci-gates.md](docs/ci-gates.md).

Two failure modes it won't catch: environment-specific timing flakes that
only manifest on loaded Linux CI runners (not this machine), and true
Linux-only bugs unless you pass `--linux`.

## Task Management
- Use github issues to track tickets
- **Never open an issue nobody asked for.** Filing into the tracker is an
  outward-facing action, and the maintainer decides what gets tracked. A
  finding worth recording goes in the final answer, the PR body, or a comment
  on the issue already in play — name it there and ask before filing. This
  binds transitively: never put "open a follow-up issue" in a subagent brief
  unless that issue was requested, or it lands in the tracker without the
  maintainer ever seeing the instruction that created it.
- Break down larger tasks into tasks using a task tool (e.g. todowrite in opencode or TaskCreate in claude code)
- An agent picking up an issue should self-assign before starting work
  (`gh issue edit <N> --add-assignee @me`), so others can see it's actively
  being worked — `ir:exec` does this after it validates the triage plan
