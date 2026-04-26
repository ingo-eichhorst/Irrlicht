@AGENTS.md

# Irrlicht — Development Guide

## Project Structure

- `core/` — Go daemon and CLI tools (module: `irrlicht/core`)
- `platforms/` — Different frontends like Web and Swift
- `site/` — Landing page and documentation (static HTML, GitHub Pages)

## Build Artifacts

Use `./.build` for build artifacts.

## Key Conventions

- Go code follows hexagonal architecture: `domain/` → `ports/` → `adapters/` → `application/services/`
- Three session states only: `working`, `waiting`, `ready` — no cancelled state
- Errors are logged via `Logger` interface, not propagated with `fmt.Errorf`
- Child sessions (subagents and background agents) use `ParentSessionID` for parent-child linking

## Testing

Before marking a ticket done, run the full suite — all three layers must pass:

- Unit + e2e: `go test ./core/... -race -count=1`
- Replay: `tools/replay-fixtures.sh`
