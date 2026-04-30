@AGENTS.md

# Irrlicht — Development Guide

## Project Structure

- `core/` — Go daemon and CLI tools (module: `irrlicht/core`)
- `platforms/` — Different frontends like Web and Swift
- `site/` — Landing page and documentation (static HTML, GitHub Pages)

## Build Artifacts

Use `./.build` for build artifacts.

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

## Testing

Before marking a ticket done, run the full suite — all three layers must pass:

- Unit + e2e: `go test ./core/... -race -count=1`
- Replay: `tools/replay-fixtures.sh`
