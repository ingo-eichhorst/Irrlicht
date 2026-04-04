# Irrlicht — Development Guide

## Project Structure

- `core/` — Go daemon and CLI tools (module: `irrlicht/core`)
- `platforms/macos/` — Swift macOS menu bar app (SwiftPM)
- `site/` — Landing page and documentation (static HTML, GitHub Pages)

## Build Artifacts

**Do not create `bin/` or build outputs in the project tree.** Use `/tmp/` for build artifacts.

```bash
# Go daemon
cd core && go build -o /tmp/irrlichd ./cmd/irrlichd

# CLI tool
cd core && go build -o /tmp/irrlicht-ls ./cmd/irrlicht-ls

# Swift app (uses .build/ which is gitignored)
cd platforms/macos && swift build
```

The only legitimate build directory is `platforms/macos/.build/` (managed by SwiftPM, gitignored).

## Testing

```bash
cd core && go test ./... -count=1
```

## Release Process

Use `/ir:release` (patch), `/ir:release minor`, or `/ir:release major`. See `.claude/skills/ir:release/skill.md` for the full pipeline.

## Key Conventions

- Go code follows hexagonal architecture: `domain/` → `ports/` → `adapters/` → `application/services/`
- SessionDetector delegates to: `StateClassifier`, `MetadataEnricher`, `PIDManager`
- Three session states only: `working`, `waiting`, `ready` — no cancelled state
- Errors are logged via `Logger` interface, not propagated with `fmt.Errorf`
- Child sessions (subagents) use `ParentSessionID` for parent-child linking
