# Contributing to Irrlicht

Thanks for considering a contribution. Irrlicht is MIT-licensed and welcomes
bug reports, feature ideas, documentation fixes, and code.

The full contributor guide lives at
[irrlicht.io/docs/contributing.html](https://irrlicht.io/docs/contributing.html).
This file is the short version that GitHub surfaces directly on the repo.

## Ways to contribute

- **Report bugs** — [open an issue](https://github.com/ingo-eichhorst/Irrlicht/issues/new/choose)
- **Request an adapter** — use the *Adapter request* issue template
- **Discuss ideas** — [GitHub Discussions](https://github.com/ingo-eichhorst/Irrlicht/discussions)
- **Send a PR** — small, focused changes get merged fastest
- **Improve docs** — fixes to the site under `site/docs/` and to this repo's markdown are always welcome

First time? Look for issues labeled
[`good first issue`](https://github.com/ingo-eichhorst/Irrlicht/labels/good%20first%20issue),
then start with `/ir:exec` — see "Learn by directing an agent" below.

## Learn by directing an agent

Irrlicht doubles as a hands-on learning platform — the skill on offer is
*directing a coding agent to build software*, not learning to use the
Irrlicht app itself. This repo's own contribution loop is the practice ground,
and `/ir:exec` is the starting point for it:

- **Start here.** `/ir:exec <issue-number>` runs the full loop end to end —
  investigate → plan → (approve →) implement → review → PR — deciding how
  much oversight to ask for from the issue itself (`/ir:exec plan <N>` always
  asks first, if you'd rather control that explicitly). This is the practice
  rep, whether you're a maintainer or a first-time contributor. Issues labeled
  [`good first issue`](https://github.com/ingo-eichhorst/Irrlicht/labels/good%20first%20issue)
  or [`ready-for-agent`](https://github.com/ingo-eichhorst/Irrlicht/labels/ready-for-agent)
  are low-stakes starting reps.
- **See it work first.** [`examples/`](examples/) has runnable,
  no-toolchain-needed demos of multiple agents operating on real sessions:
  `relay/`, `roundtrip/`, and `coding-factory/`.
- **You don't need to read a diff to contribute.** Describing a problem
  clearly in an issue or Discussion is a complete contribution — `/ir:triage`
  turns it into something an agent can pick up.

## Development setup

Prerequisites: macOS 13+, Go 1.25+, Swift 5.9+, Xcode Command Line Tools.

```bash
git clone https://github.com/ingo-eichhorst/Irrlicht.git
cd Irrlicht
./tools/build-release.sh       # build daemon + macOS app
go test ./core/... -race -count=1   # run the Go suite (see AGENTS.md for the full list)
tools/install-git-hooks.sh     # one-time: wire the pre-push preflight check
```

The full suite — Go unit + e2e, the onboarding-factory, replay fixtures, and the
web suites — is listed in [AGENTS.md](AGENTS.md#testing). **A change is only done
when every layer passes.** No exceptions.

Project layout:

```
core/        Go daemon (hexagonal: domain → ports → adapters → services)
platforms/   Swift macOS app, web frontend
site/        Landing page and docs (GitHub Pages)
tools/       Build, release, and replay-fixture scripts
replaydata/  Sample transcripts and recorded sessions (onboarding-factory fixture catalog)
```

See [AGENTS.md](AGENTS.md) for the architectural conventions every change is
expected to follow.

## Pull request workflow

1. Fork and branch from `main` using a prefix: `feat/`, `fix/`, `docs/`, `refactor/`, `test/`.
2. Keep the change focused — one concern per PR.
3. Add tests for new behavior; prefer table-driven tests in Go.
4. Run the full test suite locally (see [AGENTS.md](AGENTS.md#testing)).
5. Push and open a PR. Fill in the PR template (summary, test plan, screenshots for UI work).
   The `ARS Architecture Gate` CI check flags an architecture-health
   (`core/`) regression vs `main` — advisory, not required to merge; treat a
   red result as a prompt to look closer (see [AGENTS.md](AGENTS.md#testing)
   for the local `tools/preflight.sh` equivalent).
6. Expect review feedback. Small PRs merge faster.

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(core): add WebSocket reconnection with backoff
fix(macos): correct state transition on ESC cancellation
docs(adapters): clarify Gas Town polling cadence
```

Keep the first line under 72 characters, explain *why*, and reference issues
when relevant (`Fixes #42`).

## Code guidelines

**Go (daemon).** Follow `gofmt`/`go vet`. Errors are logged via the `Logger`
interface, not propagated with `fmt.Errorf` for observability-only failures.
Adapter packages own their format-specific parsers — don't move parsing into
shared code. Three session states only: `working`, `waiting`, `ready` (no
`cancelled`).

**Swift (app).** Follow the
[Swift API Design Guidelines](https://www.swift.org/documentation/api-design-guidelines/).
Keep SwiftUI views small and composable; use previews for visual tests.

**General.** Don't add abstractions ahead of need. Delete unused code rather
than commenting it out. Comments explain *why*, not *what*. Prefer editing
existing files over creating new ones.

## Reporting bugs

Before filing, check existing issues and confirm you're on the latest release.
Daemon logs live at `~/Library/Application Support/Irrlicht/logs/` — including
a relevant excerpt in your report helps a lot.

Please use the **Bug report** issue template. It asks for macOS version,
Irrlicht version (`irrlichd --version`), repro steps, expected vs. actual
behavior, and logs.

## Feature requests

Open a [Discussion](https://github.com/ingo-eichhorst/Irrlicht/discussions)
first to gauge interest. Describe the *problem* you're solving, not just the
solution, and consider how it fits the project philosophy: zero-config,
deterministic, honest signals.

## AI agent contributors

If you're an AI coding agent working on this repo:

- Run the full test suite (see [AGENTS.md](AGENTS.md#testing)) after every change. A task is only complete when every layer passes.
- Never mark a task done based on compilation alone.
- If validation fails, find the root cause. Don't skip, comment out, or weaken assertions.
- Record session semantics in adapter parsers, not in shared tailer code.

## Security

Please don't file security issues in public. See [SECURITY.md](SECURITY.md)
for the private reporting process.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
