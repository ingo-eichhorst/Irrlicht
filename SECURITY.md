# Security Policy

## Supported versions

Irrlicht is a single-artifact macOS app and ships as rolling releases. Only
the **latest release** published on
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
- The Irrlicht version (`irrlichd --version`) and macOS version
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

## Scope

In scope:

- The Go daemon (`irrlichd`) and its HTTP/WebSocket API on port 7837
- The Swift macOS app and its IPC with the daemon
- Local state files under `~/Library/Application Support/Irrlicht/`
- Transcript parsing in the agent adapters (Claude Code, Codex, Pi, Gas Town)
- Build and release scripts in `platforms/`

Out of scope:

- Vulnerabilities in upstream coding agents (Claude Code, Codex, Pi, Gas Town)
  themselves — please report those to their respective projects
- Issues that require an attacker already running code as your user on your Mac
- Social-engineering or physical-access scenarios
- Findings from automated scanners without a demonstrated impact

## Threat model, briefly

Irrlicht is local-first. The daemon reads transcript files the user already
has on disk and exposes state on `127.0.0.1:7837`. It does not open any
external network ports and does not transmit transcript contents off the
machine. Security-relevant areas include: any path traversal or TOCTOU around
transcript watching, the loopback HTTP/WS surface, file-permission handling
on the state directory, and the daemon-spawning logic in the macOS app.

Thanks for helping keep Irrlicht users safe.
