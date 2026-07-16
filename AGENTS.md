# Irrlicht — Development Guide

## Short Cuts

- Issue execution is handled entirely by the `ir:exec` skill:
  `/ir:exec [mode] <N>` (mode defaults to `auto`) — see its "Modes" section
  (`.claude/skills/ir:exec/SKILL.md`) for what each mode does.
- NEVER RUN: the Workflow tool (multi-agent orchestration) if not explicitly requested (too expensive)

## Process Rules

Worktrees share the parent repo's `.git` dir, so **`git stash` is not isolated per worktree** — it's a single shared stack. Concurrent agents stashing in different worktrees can pop each other's WIP. Use a local commit as a checkpoint instead (`git commit -m wip`, amend/reset later).
When encountering suboptimal processes or issues make improvment suggestions in you final answer to the user.

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
- Three session states only: `working`, `waiting`, `ready` — no cancelled state
- Errors are logged via `Logger` interface, not propagated with `fmt.Errorf`
- Child sessions (subagents and background agents) use `ParentSessionID` for parent-child linking
- Adapters declare `Permissions` on `agent.Agent` (with Apply/Remove effect closures); every read or modification an adapter performs must be consent-gated behind one of its declared permissions — nothing is exercised while pending or denied

## Testing

Before marking a ticket done, run the full suite — every layer must pass:

- Unit + e2e: `go test ./core/... -race -count=1` (includes the headless
  daemon startup smoke test — boots a real `irrlichd` on an ephemeral port
  under `t.TempDir()`, so it never touches the production daemon).
- Architecture: `core/architecture_test.go` (runs automatically as part of
  `go test ./core/...`) statically enforces the hexagonal import direction
  from Key Conventions — `domain/` and `ports/` packages may not import
  outward into `adapters/` or `application/`, and `application/services/`
  may only reach `adapters/inbound/` through `ports/`.
- Architecture score: `tools/ars-gate.sh` flags it when the Agent Readiness
  Score (composite or any category) regresses vs `origin/main` — advisory,
  not a merge gate: it runs as a PR check (`.github/workflows/ars-gate.yml`,
  not required by branch protection) and is mirrored locally by
  `tools/preflight.sh`'s `arch` gate (see "Local CI parity" below). A
  red result is a prompt to look closer, not a block — use judgment on
  whether the regression is worth addressing before merging. Deterministic
  and workflow-agnostic: it fires on any push, not tied to a specific agent
  skill.
- Code health: CodeScene posts a "CodeScene Code Health Review" check on every
  PR automatically (via the CodeScene GitHub App, configured on codescene.io
  project 82148 — not a workflow in this repo). Like the ARS score, it's
  advisory, not a merge gate: neither branch protection nor the "Protect
  Main" ruleset requires it to pass. A red result is a prompt to look
  closer, not something to chase to green before merging or releasing. The
  README's CodeScene badge shows the live score, auto-refreshed on every
  push to `main` by `.github/workflows/codescene-badge.yml`. For concrete,
  file:line-level findings (rule, message, fix effort) rather than a
  hotspot/trend view, run `/ir:sonarqube-report`, which reads SonarQube
  Cloud's issue list via `tools/sonarqube-report.sh` (needs `SONAR_TOKEN`
  in a local `.env` — see `.env.example`).
- Permission gating: `contracttesting.AssertPermissionGated` (`core/internal/contracttesting/permission_gate.go`)
  is the behavioral counterpart to the architecture test — it can't be checked
  statically because gating happens at runtime, by an adapter (or the shared
  services layer) choosing to call `PermissionService.Granted`/`ObserveGranted`
  before a read/write, or by wiring a permission's `Apply`/`Remove` closures.
  New adapters should wire it into their test suite for every `modify`-kind
  permission they declare — see `claudecode`'s hooks/statusline (a live
  per-request `ConsentGate`), `claudecode`'s instructions and `processlifecycle`'s
  kitty remote-control (install-type `Apply`/`Remove`), and `InputService`'s
  backchannel forwarding (the shared "control" gate) for the three call-site
  shapes it covers.
- Factory: `go test ./tools/onboarding-factory/... -race -count=1`.
- Replay: `tools/replay-fixtures.sh`
- Replay goldens (when a recording or replay-output change is in play):
  regenerate with `UPDATE_REPLAY_GOLDENS=1 go test
  ./tools/onboarding-factory/cmd/replay/... -count=1` (the `-count=1`
  matters — without it the cached test result skips the write). Commit only
  the goldens of the adapter you touched.
- replaydata integrity (the onboarding recording/fixture catalog): `go run ./tools/onboarding-factory/cmd/of validate`
  (schema + referential integrity over the catalog + cells — a CI gate). When a
  `web/` or recording-rig change is in play, also `bash
  tools/onboarding-factory/scripts/smoke-test.sh` (the rig's `bash -n` + lib
  unit tests).
- Web (only when touching a `web/` tree): `npm test` in that tree. There are
  two independent suites, each with its own `node_modules`:
  - `platforms/web/` — the dashboard.
  - `tools/onboarding-factory/internal/viewer/web/` — the onboarding viewer.

  `npm test` runs `vitest run` (single CI-shaped pass, no watch).

### Local CI parity — catch failures before pushing

`tools/preflight.sh` runs every PR-gating check (test.yml + web-test.yml +
ars-gate.yml, plus the full Linux build+test+replay-fixtures gate via Docker
under `--linux`) locally, in CI's order, and prints a pass/fail summary
instead of stopping at the first failure — so before opening a PR, run it
once instead of round-tripping through GitHub Actions per fix:

```
tools/preflight.sh                # everything except the Linux Docker gate
tools/preflight.sh --linux        # + full Linux parity (slow: needs Docker)
tools/preflight.sh --only go      # just the test.yml-equivalent gates
tools/preflight.sh --only arch    # just the ARS architecture gate
```

`tools/install-git-hooks.sh` (run once per clone; worktrees share the parent
repo's hooks automatically) wires `tools/preflight.sh`'s fast gates as a
pre-push hook, so a push that would fail CI is rejected locally instead.
Skip once with `git push --no-verify`.

Two of the failure modes it won't catch: environment-specific timing flakes
that only manifest on loaded Linux CI runners (not this machine), and true
Linux-only bugs unless you pass `--linux`.

## Task Management
- Use github issues to track tickets
- Break down larger tasks into tasks using a task tool (e.g. todowrite in opencode or TaskCreate in claude code)
- An agent picking up an issue should self-assign before starting work
  (`gh issue edit <N> --add-assignee @me`), so others can see it's actively
  being worked — `ir:exec` does this automatically at the start of its
  implement phase
