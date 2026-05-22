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

The dashboard is a single file: `platforms/web/index.html`. Edit it in
place; no codegen, no embed, no second copy in the repo.

The daemon serves it from disk at runtime. `resolveUIDir` in
`core/cmd/irrlichd/main.go` searches in order:

1. `$IRRLICHT_UI_DIR` (escape hatch for unusual setups)
2. `<exe>/../Resources/web/` — production .app bundle layout
3. `~/.local/share/irrlicht/web/` — daemon-only curl install
4. Walk up from the executable for `platforms/web/` — dev checkout

`tools/build-release.sh` copies the file into both
`Irrlicht.app/Contents/Resources/web/` and the
`irrlichd-darwin-universal.tar.gz` artifact. `site/install.sh --daemon-only`
extracts the tarball into `~/.local/share/irrlicht/web/`.

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

Before marking a ticket done, run the full suite — all three layers must pass:

- Unit + e2e: `go test ./core/... -race -count=1`
- Replay: `tools/replay-fixtures.sh`
