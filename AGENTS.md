# Irrlicht — Development Guide

## Short Cuts

- Issue execution is handled entirely by the `ir:exec` skill:
  `/ir:exec [mode] <N>` (mode defaults to `auto`) — see its "Modes" section
  (`.claude/skills/ir:exec/SKILL.md`) for what each mode does.
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
- Three session states only: `working`, `waiting`, `ready` — no cancelled state
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

**A defect test proves nothing until it has been seen red.** Run it before the fix
exists, confirm it fails, paste the failure. A test that passes on `main` means either
the diagnosis is wrong or the test doesn't reach the defect (a stub blind to the
asserted field is the classic) — stop and report rather than shipping the green. Locks
— tests pinning behavior that must *not* change — pass by construction; say which ones
those are. `ir:exec` enforces this at Phase 4 step 11a; it binds outside `ir:exec` too.

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
- Hook endpoints: `contracttesting.AssertHookEndpointFollowsBindAddr`
  (`core/internal/contracttesting/hook_endpoint.go`) is the same kind of
  runtime obligation for adapters that install hooks into a JSON config —
  an install writes the resolved port not `:7837`, an entry left by a daemon
  on another port is rewritten in place rather than duplicated, and uninstall
  is not port-scoped (#1178). A new hook-installing adapter wires one call
  (see `claudecode`/`codex` `hookport_test.go`) instead of porting a test file.
- Hook disclosure: `contracttesting.AssertHookDisclosureMatchesInstalled`
  (`core/internal/contracttesting/hook_disclosure.go`) binds a hooks
  permission's consent copy to what the installer actually writes — the
  `Touches`/`Detail` text names every event in `installedHookEvents`, states
  the right entry count, and names no event the adapter doesn't install
  (#1356). It exists because that text *is* the #570 consent contract and it
  was hand-maintained: Claude Code's copy declared six entries for the whole
  of #1173's seven-event install, so the Notification hook was written to the
  user's `settings.json` undisclosed. Adapters now derive both the count and
  the list from `installedHookEvents` via `hookjson.EventList`, and wire one
  call (see `claudecode`/`codex` `hookdisclosure_test.go`). The "names no
  uninstalled event" arm checks against `session.AllHookEvents`, itself kept
  honest by `TestAllHookEvents_CoversEveryConstant`, which scans
  `hook_signal.go`'s source rather than trusting a second hand-kept list.
- Hook path confinement: `contracttesting.AssertHookPathConfined`
  (`core/internal/contracttesting/hook_path_confinement.go`) is the
  receiving-side counterpart to the two above — the `transcript_path` in an
  inbound hook body is caller-supplied on a local, unauthenticated endpoint, so
  a receiver confines it to the adapter's own declared transcript roots
  (`agent.Source`'s `FilesUnderRoot.AllRootsFor`, never a second list that can
  drift) before anything downstream opens it. Six obligations: an in-tree path
  is still accepted (the vacuity guard); an out-of-tree path, a `..` traversal,
  a symlink planted inside the root and a *dangling* symlink inside the root
  are each refused, logged and counted; and the adapter's production
  constructor confines, not merely the handler the test assembled.
  Two are load-bearing and neither is obvious. **Symlinks are resolved BEFORE
  the containment check** — a guard with that order reversed passes every
  lexical traversal test and confines nothing (#1361, where claudecode
  forwarded the raw path while codex confined). And **"unresolvable" is not
  "not written yet"**: resolution reports the same error for a broken link as
  for an absent file, so a receiver that waves through an unresolvable leaf
  (a reasonable allowance — the hook fires around the write) lets an attacker
  plant a broken link, have it accepted, then create the target. A new
  hook-receiving adapter wires one call (see `claudecode`/`codex`
  `hookpath_test.go`; claudecode wires it twice, because its statusline
  endpoint is a receiver too and was the one the original fix forgot).
  **A confinement refusal answers 2xx** — it is reported by the log and the
  counter, never by a status code on the user's critical path. The path is
  already contained by not being forwarded, so the status buys no security,
  while a non-2xx is an untested interaction with the agent CLI: Claude Code
  documents that a non-2xx from a `type: http` hook "can't block actions" but
  not whether it surfaces an error, and gemini-cli's pre-tool hooks and
  Copilot's `preToolUse` are known to fail CLOSED on an error result. The
  contract asserts the 2xx so a new receiver inherits the rule instead of
  re-deciding it (#1361, #1364). `hookjson.RejectPath` is the single place it
  is implemented, and its doc comment records what was and was not observed.
- Hook version floors: `contracttesting.AssertHookVersionGate`
  (`core/internal/contracttesting/hook_version.go`) is the static half of
  #1365 — a hooks permission declares the minimum upstream CLI version its
  install requires (`agent.HookInstall.Version`), that floor is at or above
  every installed event's own `Since` entry, and it actually refuses one patch
  below itself with a reason naming both versions. The runtime half — that
  `PermissionService.runClosureEffect` consults the declaration before running
  `Apply` — is `core/application/services/permission_version_gate_test.go`, the
  same split `AssertPermissionGated` draws. It exists because Codex carried a
  private `codexSupportsHooks` with its own parser and floor constants while
  Claude Code carried nothing and wrote seven entries into the user's
  `settings.json` at any version; a third adapter joins by declaring
  `Version: &agent.VersionGate{Min: "x.y.z", Probe: []string{"<cli>",
  "--version"}}` and nothing else. `TestEveryHookInstallDeclaresAVersionFloor`
  (`core/adapters/inbound/agents/hookversion_test.go`) walks the registry
  projection so a new hook adapter is covered by existing rather than by
  remembering to wire the contract. Note the direction: an unknown or
  unparseable version fails **open** (`core/pkg/cliversion`) — the daemon runs
  under launchd with a minimal PATH and routinely cannot see the user's CLI, so
  only a version successfully read AND parsed AND found below the floor blocks
  an install.
  All five contract families pass by construction against a correct adapter, so
  their whole value is that they *can* fail: a new or reworked contract
  assertion lands with the deliberate mutation that was seen red for each
  obligation recorded in its PR — the same bar the red-first rule above sets
  for defect tests.
- Skill files: `tools/skill-lint.sh` reads every `.md` under
  `.claude/skills/` plus any other tracked `SKILL.md` (there is one under
  `tools/irrlicht-design-system/`) — the files that tell agents how to triage,
  plan, implement and review, and which had no mechanical coverage at all
  until #1209 (PR #1204 changed two of them, `preflight.sh --changed` skipped
  all ten gates, and thirteen PR checks went green having read nothing that
  changed). Unresolved conflict markers, leftover `{{TOKEN}}` / `REPEAT:` /
  `OPTIONAL:` template scaffolding and an unbalanced code fence are hard
  failures; the internal-reference, list-count and frontmatter checks are
  heuristics and only warn until their noise floor is known (`--strict`
  promotes them, which is how one gets hardened). The fence and frontmatter
  checks exist because skipping is how the linter tells "documents a marker"
  from "has one" — so an unbalanced delimiter would otherwise silence the rest
  of the file, and the rule is that when a file cannot be parsed with
  confidence the linter degrades toward *more* checking, never less. Runs as
  its own `skill-file lint` gate in `tools/preflight.sh` (scoped to skill
  markdown plus the linter itself) and unscoped as test.yml's "Lint skill
  files" step — first in the job, before `setup-go`. Its own tests are
  `tools/lib/skill-lint_test.sh`, over the fixture corpus under
  `tools/lib/testdata/skill-lint/` — so the assertions never move when a real
  skill file is edited, and `testdata/` is excluded from the gate's own walk
  because those fixtures are deliberately corrupt.
- Factory: `go test ./tools/onboarding-factory/... -race -count=1`.
- Replay: `tools/replay-fixtures.sh` — gated in CI by linux.yml, and run
  natively as `tools/preflight.sh`'s `replay fixtures` gate, so golden drift
  surfaces without Docker. Takes ~3 minutes unscoped; under `--changed` it
  runs only when the diff touches `replaydata/`, the factory, or the
  `core/` parsers and tailer a golden is derived from.
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

  `node_modules/` is gitignored, so a fresh clone — or any new
  `git worktree add` — starts without dependencies. No manual install step is
  needed: each tree's `pretest` script runs `npm ci --ignore-scripts` when
  they're missing, so `npm test` self-heals on its first run (slow once,
  instant afterwards). To get it out of the way up front, run `npm ci` in the
  tree yourself. **Never `npm install`** — it re-resolves the dependency graph
  and rewrites `package-lock.json`; on an npm older than the one that wrote the
  lockfile it silently strips the `libc` fields from the
  `@rolldown/binding-linux-*` entries, and that churn then rides along in an
  unrelated PR. `npm ci` installs *from* the lockfile and never writes it.

  Because the two trees resolve independently, they can drift onto different
  versions of the same transitive package — which is how one ended up carrying
  a vulnerable `postcss` while the other was patched (#1225). Both are kept
  current by weekly dependabot updates configured in `.github/dependabot.yml`,
  which covers the Go modules and GitHub Actions on the same schedule; a bump
  landing in only one tree is a signal something is wrong with that config, not
  normal.

### Local CI parity — catch failures before pushing

`tools/preflight.sh` runs every PR-gating check (test.yml + web-test.yml +
ars-gate.yml + linux.yml's replay-fixtures step natively, plus the full Linux
build+test gate via Docker under `--linux`) locally, in CI's order, and prints
a pass/fail summary
instead of stopping at the first failure — so before opening a PR, run it
once instead of round-tripping through GitHub Actions per fix:

```
tools/preflight.sh                # everything except the Linux Docker gate
tools/preflight.sh --linux        # + full Linux parity (slow: needs Docker)
tools/preflight.sh --only go      # just the test.yml-equivalent gates
tools/preflight.sh --only arch    # just the ARS architecture gate
tools/preflight.sh --only skills  # just the .claude/skills/**/*.md linter
```

`tools/install-git-hooks.sh` (run once per clone; worktrees share the parent
repo's hooks automatically) wires `tools/preflight.sh`'s fast gates as a
pre-push hook, so a push that would fail CI is rejected locally instead. The
hook runs `tools/preflight.sh --changed`, which scopes every gate to the
packages and web trees the push's diff actually touches (vs `origin/main`), so
a typical push finishes in seconds rather than re-running the whole suite —
important for automated callers, whose bounded command timeout the full run
routinely blew past, killing the push after the commit had already landed. A
large or cross-cutting diff (or a `go.mod`/`go.sum` change, which falls back to
the full core suite) can still take a few minutes. Skip once with `git push
--no-verify`; run `tools/preflight.sh` manually (no `--changed`) for the
unscoped full gate.

The security gate is scoped twice over: its trigger regex decides whether the
scan runs at all, and `tools/security-scan.sh --changed` then picks which Go
modules and web trees to scan, matching each scanner against the files it
actually reads. Without that second layer a pure-Go push paid for an `npm
audit` of both web trees and was rejected by a pre-existing advisory it could
not have caused (#1213) — forcing `--no-verify`, which disables every other
gate too. Both layers read the same changed set, from
`tools/lib/changed-files.sh`; its unit tests run in the `tools` gate.

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
