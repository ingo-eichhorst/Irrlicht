# Irrlicht — Development Guide

## Short Cuts

- "wt+plan" = create a worktree for the following issue. move into the worktree and plan execution
- "wt+exec" = create a worktree for the following issue. move into the worktree, plan and directly execute
- "wt+full" = create a worktree for the following issue. move into the worktree, plan, directly execute and start the code-review skill on low, then fix all issues and open a PR.
- "wt+close" = make sure all contents of the worktree are pushed to a pr. Then close the worktree, move back to main and delete the worktree

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

`tools/preflight.sh` runs every PR-gating check (test.yml + web-test.yml,
plus the full Linux build+test+replay-fixtures gate via Docker under
`--linux`) locally, in CI's order, and prints a pass/fail summary instead of
stopping at the first failure — so before opening a PR, run it once instead
of round-tripping through GitHub Actions per fix:

```
tools/preflight.sh                # everything except the Linux Docker gate
tools/preflight.sh --linux        # + full Linux parity (slow: needs Docker)
tools/preflight.sh --only go      # just the go-test.yml-equivalent gates
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
