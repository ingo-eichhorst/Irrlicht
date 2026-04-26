<div align="center">

# ✦ Irrlicht — Menu-Bar Lights for AI Coding Agents (macOS)

![Banner](assets/banner.png)

[![Coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fgist.githubusercontent.com%2Fingo-eichhorst%2F9f14c8e5f25c1ccf5d6500c1685fd9fb%2Fraw%2Fcoverage.json&color=%238B5CF6)](https://github.com/ingo-eichhorst/Irrlicht/actions/workflows/coverage.yml)
[![License](https://img.shields.io/badge/license-MIT-orange?color=%23FF9500)](LICENSE)
[![Version](https://img.shields.io/badge/dynamic/json?url=https%3A%2F%2Fraw.githubusercontent.com%2Fingo-eichhorst%2FIrrlicht%2Fmain%2Fversion.json&query=%24.version&label=version&color=%2334C759)](version.json)
[![ARS](https://img.shields.io/badge/ARS-Agent--Ready%208.2%2F10-green)](https://github.com/ingo-eichhorst/agent-readyness)

[🌐 Landing Page](https://ingo-eichhorst.github.io/Irrlicht/) · [📖 Documentation](https://ingo-eichhorst.github.io/Irrlicht/docs/quickstart.html) · [📦 Latest Release](https://github.com/ingo-eichhorst/Irrlicht/releases/latest)

</div>

> 🟣 working · 🟠 waiting · 🟢 ready — one ambient dot per session, multi-agent, no hooks.

![UI Features](assets/irrlicht-explainer.png)

## Install

**Homebrew (recommended):**

```sh
brew tap ingo-eichhorst/irrlicht
brew install --cask irrlicht
```

**curl:**

```sh
curl -fsSL https://irrlicht.io/install.sh | sh
```

**DMG:** download `Irrlicht-<version>.dmg` from [Releases](https://github.com/ingo-eichhorst/Irrlicht/releases) and drag **Irrlicht.app** to **Applications**.

→ [Quickstart](https://ingo-eichhorst.github.io/Irrlicht/docs/quickstart.html) gets you to your first purple dot in 60 seconds.

## What it does

- **Three-state menu-bar dot per session** — 🟣 working, 🟠 waiting, 🟢 ready
- **Context-pressure gauge** — 🟢 → 🟡 → 🔴 → ⚠️ before the auto-compact cliff, so you can `/compact` while quality is still intact
- **Live per-session cost in USD** — model-aware via LiteLLM pricing
- **Subagent trees** — parent ↔ Explore / Plan / background agents, not just the parent
- **Git-aware grouping** — sessions clustered by project, branch, worktree
- **Real-time** — <1s latency via FSEvents/kqueue; no polling

## Supported agents

| Agent          | Status |
| -------------- | ------ |
| Claude Code    | live   |
| OpenAI Codex   | live   |
| Pi             | live   |
| Gas Town       | live   |

→ [Adapters reference](https://ingo-eichhorst.github.io/Irrlicht/docs/adapters.html) for watch paths, model detection, and roadmap.

## Posture

Local-first · no telemetry · MIT · ~5 MB RAM · signed Homebrew cask · transcripts read-only.

## Why Irrlicht (vs. the rest)

- **Quota trackers** ([ccusage](https://github.com/ryoppippi/ccusage), [ClaudeBar](https://github.com/tddworks/ClaudeBar), [SessionWatcher](https://www.sessionwatcher.com/)) count tokens, not state.
- **Observability stacks** ([Langfuse](https://langfuse.com/integrations/frameworks/claude-agent-sdk), [SigNoz](https://signoz.io/blog/claude-code-monitoring-with-opentelemetry/)) need SDK instrumentation and a dashboard tab.
- **Single-agent monitors** ([Claude Status](https://github.com/gmr/claude-status), [Agent Sessions](https://github.com/jazzyalex/agent-sessions)) lock you to one CLI or one terminal.

Irrlicht is ambient (menu bar, not a window), multi-agent (Claude / Codex / Pi / Gas Town in one vocabulary), and transcript-driven — no SDK wrappers, no OpenTelemetry collectors, no dashboard tab to keep open.

## The problem (why this exists)

> *In Goethe's Faust, an Irrlicht guides the way through the night. This one guides you through your agents — who's working, who's waiting, and where you're needed next.*

Six concrete pains, every one documented:

- **You don't know which session needs you.** Claude Code's desktop notifications [don't fire inside tmux](https://github.com/anthropics/claude-code/issues/19976) — the most common multi-session setup.
- **Parallel sessions shred your attention.** *"The mental gymnastics of context switching wears me out and makes me wonder how well I'm steering each session"* ([dev.to](https://dev.to/datadeer/part-2-running-multiple-claude-code-sessions-in-parallel-with-git-worktree-165i)).
- **Context compaction silently wrecks quality.** Auto-compact fires at ~80% of the window, but the model degrades 20–30% before that ([MindStudio](https://www.mindstudio.ai/blog/claude-code-compact-command-context-management); [GitHub issue with hundreds of reactions](https://github.com/anthropics/claude-code/issues/13112)).
- **Cost runs away in the dark.** A recent prompt-caching bug silently inflated token usage 10–20× for weeks ([The Register](https://www.theregister.com/2026/03/31/anthropic_claude_code_limits/)) — visible only on the invoice.
- **Subagents are a black box.** Spawn three Explore agents and a background task; you see the parent, not each child.
- **Quota makes you agent-hop — and monitoring doesn't follow.** Burn Claude Code by 11am, fall back to Codex or Gemini; each agent has its own vocabulary for "working", "waiting", and "done".

*(Irrlicht is German for will-o'-the-wisp — a light that guides you through the dark.)*

## How it works

```
Transcript files → FSEvents/kqueue → state machine → menu bar
```

Irrlicht reads the `.jsonl` transcripts your agents already write, persists each session as atomic JSON under `~/Library/Application Support/Irrlicht/instances/`, and renders dots in a SwiftUI menu-bar app over a local WebSocket. The app ships as a single `.app` bundle with the daemon embedded — no separate services, no version drift.

→ [Architecture](https://ingo-eichhorst.github.io/Irrlicht/docs/architecture.html) for the hexagonal pipeline and state-machine rules.

## For coding agents

Irrlicht is agent-verifiable by design — every session lives as atomic JSON at a known path, so any tool (including the coding agents themselves) can read live state and verify its own work.

- **State files:** `~/Library/Application Support/Irrlicht/instances/*.json`
- **Conventions and test gate:** [AGENTS.md](AGENTS.md)

## Next steps

[Documentation](https://ingo-eichhorst.github.io/Irrlicht/docs/) · [Installation](https://ingo-eichhorst.github.io/Irrlicht/docs/installation.html) · [Changelog](https://ingo-eichhorst.github.io/Irrlicht/docs/changelog.html) · [Issues](https://github.com/ingo-eichhorst/Irrlicht/issues) · [Discussions](https://github.com/ingo-eichhorst/Irrlicht/discussions)

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=ingo-eichhorst/Irrlicht&type=Date)](https://star-history.com/#ingo-eichhorst/Irrlicht&Date)

## License

MIT License — see [LICENSE](LICENSE).
