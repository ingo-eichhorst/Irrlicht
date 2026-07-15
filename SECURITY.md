# Security Policy

## Supported versions

Irrlicht ships as rolling releases: a single-artifact macOS app, plus a
daemon-only Linux build (no menu-bar UI). Only the **latest release**
published on
[GitHub Releases](https://github.com/ingo-eichhorst/Irrlicht/releases/latest)
receives security fixes. If you're on an older version, please update before
reporting.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report privately through one of these channels:

1. **GitHub private vulnerability reporting** (preferred) —
   [Report a vulnerability](https://github.com/ingo-eichhorst/Irrlicht/security/advisories/new)
2. **Email** — git@ingo-eichhorst.de with the subject line
   `[Irrlicht security]`

Include as much of the following as you can:

- A description of the issue and its impact
- The Irrlicht version (the daemon's `irrlichd --version`) and macOS version
- Reproduction steps or a proof-of-concept
- Any relevant log excerpts from `~/Library/Application Support/Irrlicht/logs/`
- Whether the issue is already public

## What to expect

- **Acknowledgement** within 3 business days.
- **Triage and severity assessment** within 7 days.
- **Fix timeline** communicated once the issue is understood. Critical issues
  get an out-of-band release; lower-severity issues roll into the next planned
  release.
- **Credit** in the release notes if you'd like it — let us know how you want
  to be named.

Please give us a reasonable window to ship a fix before any public disclosure.
We'll coordinate timing with you.

## Proactive scanning

Beyond the reactive process above, every release runs a security gate
(`tools/security-scan.sh`, invoked from `.claude/skills/ir:release/SKILL.md`)
before artifacts are built: open GitHub Dependabot and CodeQL alerts,
`govulncheck`, `gosec`, and `npm audit` across every Go module and web
tree. Critical/High findings abort the release. The same local checks
(without the GitHub alert queries) run via `tools/preflight.sh --only
security` as part of the pre-push gate. GitHub CodeQL default setup covers
Go and JavaScript/TypeScript continuously, independent of releases.

## Scope

In scope:

- The Go daemon (`irrlichd`) and its HTTP/WebSocket API on port 7837
- The Swift macOS app and its IPC with the daemon
- Local state files under `~/Library/Application Support/Irrlicht/`
- Transcript parsing in the agent adapters (Claude Code, Codex, Pi, Aider, OpenCode, Kiro CLI, Gemini CLI, Antigravity, Mistral Vibe) and the Gas Town orchestrator
- Build and release scripts in `platforms/`

Out of scope:

- Vulnerabilities in upstream coding agents (Claude Code, Codex, Pi, Aider,
  OpenCode, Kiro CLI, Gemini CLI, Antigravity, Mistral Vibe, Gas Town) themselves — please report those to their respective projects
- Issues that require an attacker already running code as your user on your Mac
- Social-engineering or physical-access scenarios
- Findings from automated scanners without a demonstrated impact

## Threat model, briefly

Irrlicht is local-first and consent-first. The daemon reads and modifies
nothing until the user grants each per-agent permission through the
permission wizard (v0.5.0+); revoking a permission actively undoes it
(hooks uninstall, watchers stop). Once granted, the daemon reads transcript
files the user already has on disk and exposes state on `127.0.0.1:7837`
by default. It does not transmit transcript contents off the machine.
Security-relevant areas include: any path traversal or TOCTOU around
transcript watching, the loopback HTTP/WS surface, file-permission handling
on the state directory (including `permissions.json`), and the
daemon-spawning logic in the macOS app.

### Network exposure (opt-in)

- `IRRLICHT_BIND_ADDR` overrides the default loopback bind (e.g.
  `0.0.0.0:7837` to expose on the LAN). Use with care — state files and
  session metadata become reachable to anyone who can reach that address.
  See [the configuration docs](https://irrlicht.io/docs/configuration.html#when-to-use)
  for the LAN-exposure recipe and tradeoffs.
- `IRRLICHT_MDNS=1` enables mDNS/Bonjour advertisement of `_irrlicht._tcp`
  on the local network. Off by default. Paired with `IRRLICHT_BIND_ADDR` in
  the [configuration docs example](https://irrlicht.io/docs/configuration.html#when-to-use).
- The WebSocket endpoint (`/api/v1/sessions/stream`) rejects cross-site
  handshakes: only requests from loopback origins (or with no `Origin`
  header, as native clients send) are accepted. This holds even when the
  daemon is bound to a non-loopback address.
- Planned: both knobs will be retired in favor of an explicit hub mode.
  See the [Relay Server design](https://github.com/ingo-eichhorst/Irrlicht/wiki/Relay-Server).

Thanks for helping keep Irrlicht users safe.
