@AGENTS.md

# Irrlicht — Development Guide

## Project Structure

- `core/` — Go daemon and CLI tools (module: `irrlicht/core`)
- `platforms/` — Different frontends like Web and Swift
- `site/` — Landing page and documentation (static HTML, GitHub Pages)

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

## Web frontend

The dashboard lives in `platforms/web/` as three files — edit them in
place; no codegen, no embed, no second copy in the repo:

- `index.html` — markup only; references the two siblings via `<link>`/`<script type="module">`
- `irrlicht.css` — all styles
- `irrlicht.js` — all logic (ES module; pure helpers exported for unit tests)

The daemon serves the whole directory from disk at runtime. `resolveUIDir`
in `core/cmd/irrlichd/main.go` searches in order:

1. `$IRRLICHT_UI_DIR` (escape hatch for unusual setups)
2. `<exe>/../Resources/web/` — production .app bundle layout
3. `~/.local/share/irrlicht/web/` — daemon-only curl install
4. Walk up from the executable for `platforms/web/` — dev checkout

`tools/build-release.sh` copies all three files into both
`Irrlicht.app/Contents/Resources/web/` and the
`irrlichd-darwin-universal.tar.gz` artifact. `site/install.sh --daemon-only`
extracts the tarball into `~/.local/share/irrlicht/web/`.

### Two frontends, one authoritative

There are two web trees, and they are NOT competing copies:

- **`platforms/web/`** — the authoritative **live session dashboard**, for
  both the daemon and the onboarding viewer. The viewer's `/dashboard`
  route reads it from disk at request time (so dashboard edits need no
  viewer rebuild) and injects a tiny non-invasive `ws-diag` console-logging
  script before `</head>` via `injectBeforeClosingTag` — it never edits the
  production markup.
- **`tools/onboarding-factory/internal/viewer/web/`** — the viewer's OWN
  embedded SPA: the **catalog / scenario browser** (coverage matrix,
  per-cell detail, recording archives). It is never the live session view.

When changing the live dashboard, edit `platforms/web/` only.

## Onboarding fixture matrix (the factory + per-agent cells)

The agent-onboarding coverage matrix — scenario × adapter, browsed by the
viewer's catalog SPA — is maintained entirely through the **onboarding factory**
(`tools/onboarding-factory`), whose `of` CLI is the SOLE writer of everything
under `replaydata/`. The `ir:onboarding-factory` skill (a zero-bash dispatcher +
four verbs: create-scenario / create-agent / assess / record) drives `of`; it
never edits `replaydata/` by hand. (`of validate` is the read-back gate that
enforces this — see Testing.) Data is two file kinds:

- **Catalog** — `replaydata/agents/scenarios.json`, a single object
  `{"meta": {...}, "scenarios": [...]}`. Each `scenarios[]` entry is the
  agent-agnostic spec for one matrix row, **five fields only**: `id`
  (`<section>.<index>`), `name` (kebab slug — the FK), `description`,
  `acceptance_criteria` (markdown), and `process` (markdown). No
  `requires` / `verify` / `section` / `feature` / `cross_adapter` /
  per-agent data — applicability is decided per cell by the `assess` verb, not
  by a `requires` gate. The `meta` block holds the onboarded-adapter set
  (`min_versions` keys) and per-adapter `transcript_extensions`. Written via
  `of scenario add|update`.
- **Per-agent cell** — `replaydata/agents/<adapter>/scenarios/<id>_<name>/metadata.json`.
  One (scenario, adapter) cell: a `metadata` overview tier (the three pillars
  `agent_supports` / `daemon_capability` / `driver_capability` + versions + a
  note), a `details` tier (`assessment`, `recipe`), `artifacts` refs, and a
  `scenario_id` tying the cell back to its catalog row. Written via
  `of cell write`; its spec (`expected.jsonl`) via `of cell spec`.
  **Recording folders are prefixed by the scenario's dashed id** —
  e.g. scenario `architect-editor-pair` (id `5.4`) records under
  `5-4_architect-editor-pair`. A few cells use a variant folder name (e.g. pi's
  `2-17_agent-question-pending` for `user-blocking-question`); the `scenario_id`
  field is the authoritative link, not the folder name.

**Recordings layout.** A cell folder holds only `metadata.json` (the cell
descriptor) and `expected.jsonl` (the spec) at its root. **Every recording —
newest included — lives under `recordings/<name>/`**, holding all of its own
data (`events.jsonl`, `transcript.{jsonl,md}`, `manifest.json`, and the
`transcript.jsonl.replay.json.golden`). There is no "latest" at the cell root.
Recording folder names are timestamp-prefixed
(`<iso-ts-hyphens>_irrlichd-<ver>`), so they sort newest-first by name — the
viewer lists them name-descending and autoselects the newest; the
`metadata.json` `artifacts` point at the newest recording and list all of them.
`expected.jsonl` is validated against each recording (the newest gates; older
ones are a drift signal). **`regressions/` cells keep their plain (un-prefixed)
folder names** — they're not catalog rows — but follow the same
`recordings/<name>/` layout.

`validate.NewestRecordingDir(cellDir)` resolves the newest recording; the
viewer, `cmd/expected-validate`, `replay-fixtures.sh`, `promote-recording.sh`,
and `cell-integrity.sh` all go through it. `matrix.cellRecorded` reports a cell
as recorded iff `artifacts.recordings` is non-empty.

The Go reader is `tools/onboarding-factory/internal/shard`: `LoadAll`/`Load`
read the catalog; `LoadAdapterCells` (scan one adapter, keyed by `scenario_id`),
`LoadAllCells`, and `LoadAgentCell` (direct by folder) read per-agent
`metadata.json`; `LoadMeta`/`Agents` read the `meta` block; `FolderForScenario`
computes the `<id>_<name>` folder. `internal/matrix` and the viewer both go
through it. The live-capture rig — the tmux drivers plus `run-cell.sh` /
`precheck.sh` / the gate libs, all under `tools/onboarding-factory/scripts/` —
reads cells through `tools/onboarding-factory/scripts/lib/shard-lib.sh`
(`shard_recipe`, `shard_cell`, `shard_folder`, `agent_cell_file`,
`scenario_global`, …). Agent drivers live in
`replaydata/agents/<adapter>/driver{,-interactive}.sh`, the gastown orchestrator
driver in `replaydata/orchestrators/gastown/driver.sh`, and shared driver
helpers in `replaydata/_lib/`. `of record run` resolves and drives them.

## Key Conventions

- Go code follows hexagonal architecture: `domain/` → `ports/` → `adapters/` → `application/services/`
- Three session states only: `working`, `waiting`, `ready` — no cancelled state
- Errors are logged via `Logger` interface, not propagated with `fmt.Errorf`
- Child sessions (subagents and background agents) use `ParentSessionID` for parent-child linking

## Adding a new agent adapter

Adapters are wired in one place — the `allAgents` slice in
`core/cmd/irrlichd/main.go`. Adding a new adapter is a Go-only change; no
Swift or web edits are required.

Each adapter package exports a top-level `Agent()` constructor in
`core/adapters/inbound/agents/<name>/agent.go` that returns an
`agent.Agent` (defined in `core/domain/agent/declaration.go`). The struct
has three orthogonal axes:

- **`Identity`** — `Name`, `DisplayName`, `IconSVGLight`, `IconSVGDark`.
  Served via `GET /api/v1/agents`; the macOS app and web UI look these
  up by `Name` and render automatically. No frontend code knows the
  adapter exists ahead of time. Icons are raw `<svg>…</svg>` markup
  rendered at 14×14; use the same string for both fields when the icon
  is appearance-agnostic.
- **`Process`** — `Match` (a `ProcessMatcher`: `ExactName` for
  `pgrep -x` or `CommandPattern` for `pgrep -f` against the full command
  line) plus `PIDForSession` (a `PIDDiscoverFunc` that maps cwd +
  transcript path to the owning PID).
- **`Source`** — sealed sum describing where session data lives. Pick
  one variant:
  - `FilesUnderRoot{Dir, Parser}` — append-only transcripts under a
    fixed `$HOME`-relative directory. `Parser` is `JSONLineParser{NewParser}`
    for JSONL (claudecode, codex, pi).
  - `FilesUnderCWD{Filename, Parser}` — one transcript per running
    process inside its CWD. `Parser` is `RawLineParser{NewParser}` for
    non-JSONL formats; the `RawParser` implementation must also provide
    `ParseLineRaw` and `IdleFlush` (aider).
  - `ProcessOwnedStore{PathForPID, Reader}` — session state lives in a
    structured store (SQLite). `Reader` implements `MetricsReader` and
    bypasses the JSONL-tailer path (opencode).

Parsers can opt into two refinements by implementing extra interfaces on
the same struct: `PendingContributor` (in-progress turn cost, used by
claudecode) and `SubagentCounter` (open child agents for inline-subagent
models, used by claudecode). The daemon detects these via type assertion.

Once `Agent()` exists, register the adapter by adding one line to the
`allAgents` slice in `core/cmd/irrlichd/main.go`. Everything else —
fswatcher roots, process scanners, parser-factory map, PID-discovery map
— is derived from that slice via the projections in
`core/adapters/inbound/agents/maps.go` (`Parsers`, `PIDDiscoverers`,
`ProcessNames`, `SubagentCounters`, `MetricsProviders`) and the
`Source`-variant dispatch in `core/cmd/irrlichd/wiring.go`.

## Testing

Before marking a ticket done, run the full suite — every layer must pass:

- Unit + e2e: `go test ./core/... -race -count=1` (includes the headless
  daemon startup smoke test — boots a real `irrlichd` on an ephemeral port
  under `t.TempDir()`, so it never touches the production daemon).
- Factory: `go test ./tools/onboarding-factory/... -race -count=1`.
- Replay: `tools/replay-fixtures.sh`
- replaydata integrity: `go run ./tools/onboarding-factory/cmd/of validate`
  (schema + referential integrity over the catalog + cells — a CI gate). When a
  `web/` or recording-rig change is in play, also `bash
  tools/onboarding-factory/scripts/smoke-test.sh` (the rig's `bash -n` + lib
  unit tests).
- Web (only when touching a `web/` tree): `npm test` in that tree. There are
  two independent suites, each with its own `node_modules`:
  - `platforms/web/` — the dashboard.
  - `tools/onboarding-factory/internal/viewer/web/` — the onboarding viewer.

  `npm test` runs `vitest run` (single CI-shaped pass, no watch).
