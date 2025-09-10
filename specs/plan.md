# Irrlicht – Implementation Plan (rethought)

> **Strategy:** risk‑first, contract‑driven, and incrementally shippable. Each phase has a concrete artifact, exit criteria, and test evidence.

## Guiding Principles

* **Zero‑friction:** one‑click install, no manual Claude config.
* **Local‑first:** no network egress by default.
* **Deterministic:** event‑driven from Claude Hooks; heuristics only as fallback.
* **Observable:** simple file contracts, atomic I/O, explicit logs.
* **Slim:** O(1) tailing, ≤2 s latency, ≤100 MB RSS target.

## Architecture Snapshot

```
Claude Code ──(Hooks: stdin JSON)──▶ irrlicht-hook (CLI)
                                         └─ writes JSON per session
                                         └─ logs minimal events

Irrlicht.app (SwiftUI menubar)
  ├─ watches instances/*.json
  ├─ tails transcript.jsonl (last ~64 KB) → msgs/min, tokens_in
  ├─ embedded model capacity data → context_used_%
  └─ actions: open/tail transcript, open cwd

Installer (.pkg)
  └─ installs app + CLI + LaunchAgent
  └─ merges hooks into ~/.claude/settings.json (backup/rollback)
```

---

## Phase 0 — Contracts & Drift Guard (foundations)

**Goal:** Freeze external contracts and make failures safe.

* Build **Hook Contract Fixtures** (JSON samples per event: `SessionStart`, `UserPromptSubmit`, `Notification`, `Stop`, `SubagentStop`, `SessionEnd`).
* Implement **irrlicht‑replay** (test tool) to pipe fixtures to `irrlicht-hook`.
* Implement **settings merger lib** with: JSON‑aware deep‑merge, **dry‑run**, **idempotency**, and timestamped backup/restore.
* Add **kill‑switch** (env var or settings flag) to disable hooks without uninstall.
  **Deliverables:** `fixtures/`, `irrlicht-replay`, merger lib + unit tests.
  **Exit criteria:** replay suite passes; merger is idempotent and reversible.

## Phase 1 — Event Ingestion Core

**Goal:** Receive real hook events and persist state safely.

* Implement `irrlicht-hook` (Go or Swift):

  * Parse stdin JSON; validate size (<512 KB) & fields; sanitize paths.
  * Map events → states: `working`, `waiting`, `finished`.
  * Atomic upsert of `~/Library/Application Support/Irrlicht/instances/<session_id>.json`.
  * Minimal structured log `…/logs/events.log` (rotated).
    **Deliverables:** Signed CLI binary; schema docs; unit + integration tests with fixtures.
    **Exit criteria:** e2e demo (replay → JSON files) with zero partial writes.

## Phase 2 — Tracer Bullet UI (from files)

**Goal:** Validate the menubar UX independent of Claude.

* SwiftUI `MenuBarExtra`: render **glyph strip** (●/◔/✓) from `instances/*.json`.
* Dropdown list with `shortId · state · model` and timestamps.
* File‑watcher with 200 ms debounce; finished‑TTL pruning.
  **Deliverables:** `Irrlicht.app` bundle; sample instance files.
  **Exit criteria:** Create/update/delete files → UI reflects changes ≤2 s.

## Phase 3 — One‑Click Installer & Rollback

**Goal:** Make it real for end users.

* `.pkg` installs app, CLI, app‑support, LaunchAgent (login autostart).
* Post‑install runs **merger** to add hooks to `~/.claude/settings.json` with backup.
* Uninstaller restores prior settings (or surgically removes our entries).
  **Deliverables:** Notarized `.pkg`, uninstall script, install logs.
  **Exit criteria:** Fresh box → install → new Claude session appears in UI ≤2 s; uninstall leaves system pristine.

## Phase 4 — Metrics v1 (msgs/min & elapsed)

**Goal:** First useful signal beyond state.

* Tail last \~64 KB of transcript JSONL via `transcript_path`.
* Compute **messages/min** over sliding 60 s; show **elapsed\_s**.
* Defensive parsing (resync on truncation/rotation).
  **Deliverables:** Tailer module + tests (fixtures of streaming/tool runs).
  **Exit criteria:** Replay timelines → msgs/min within expected bands; no UI stalls.

## Phase 5 — Context Utilization (%), Model Capacities

**Goal:** Surface context pressure.

* Add embedded model capacity data and loader.
* Extract **tokens\_in** from transcript (when present); otherwise estimate via char→token slope per model.
* Compute **context\_used\_%** = tokens\_in / capacity × 100; guard for unknown models.
  **Deliverables:** Capacity table, estimator, unit tests, UI wiring.
  **Exit criteria:** Known models show stable %; unknown models degrade gracefully to raw tokens.

## Phase 6 — Actions & Accessibility

**Goal:** Operability & inclusive UX.

* Actions: **Open transcript**, **Tail -f 200 lines**, **Open cwd in Terminal/VS Code**.
* VoiceOver labels for glyphs; keyboard navigation in dropdown.
  **Deliverables:** Action handlers, a11y labels, QA checklist.
  **Exit criteria:** All actions work; VoiceOver announces states correctly.

## Phase 7 — Fallback Scanner (pre‑hook sessions)

**Goal:** Useful even before hooks apply to an existing session.

* Scan `~/.claude*/projects/**/*.jsonl` periodically.
* Heuristic classification: `working` (recent growth), `waiting` (idle ≥60 s + hint), `finished` (beyond TTL).
* Mark `confidence: "low"` until first hook event arrives.
  **Deliverables:** Scanner module + tests.
  **Exit criteria:** Under replay, heuristic agrees with hook state ≥90%; never mislabels active as finished.

## Phase 8 — Hardening & Budgets

**Goal:** Production‑ready quality.

* Debounce `Notification` storms; coalesce writes.
* Enforce resource budgets (≤5% CPU p95 for ≤12 sessions; ≤100 MB RSS).
* Crash handling; atomic file discipline everywhere; log rotation.
* Code signing & notarization for both app and CLI; hardened runtime.
  **Deliverables:** Soak test report (≥2 h, 8–12 sessions), perf metrics, crash‑free runs.
  **Exit criteria:** Budgets met; no data loss; clean Gatekeeper path.

## Phase 9 — Preferences & Extensibility (optional for v1)

**Goal:** Quality of life and future hooks.

* Preferences: TTL, refresh cadence, glyph style, show/hide costs.
* Price table (optional cost estimates) + editor.
* Diagnostics panel (last 10 events, config diff viewer).
* OTLP export (opt‑in) for dashboards; **off by default**.
  **Deliverables:** Prefs UI, persisted settings, opt‑in exporters.
  **Exit criteria:** Settings persist; privacy defaults respected.

---

## Milestones & Evidence

* **M0 Foundations:** fixtures, replay tool, merger lib (P0).
* **M1 Pipeline:** hook CLI + file store + tracer UI (P1–P2).
* **M2 Install:** one‑click `.pkg` + rollback (P3).
* **M3 Metrics v1:** msgs/min + elapsed (P4).
* **M4 Context:** utilization % + capacities (P5).
* **M5 Ops UX:** actions + a11y (P6).
* **M6 Robust:** fallback scanner + hardening (P7–P8).
* **M7 QoL (opt):** prefs, diagnostics, OTLP (P9).

## Test Matrix (essentials)

* Hook mapping: each event → correct state; mixed concurrency 2/4/8 sessions.
* Atomicity: kill during write → no partial JSON; recovery path.
* Tailer: rotated/truncated files; malformed JSONL lines.
* Installer: idempotent install/uninstall; settings restored exactly.
* Perf: sustained activity; measure latency/CPU/RSS.

## Risks & Mitigations

* **Hook schema drift:** fixtures + CI contract tests; soft‑fail unknown fields.
* **Transcript variance:** tolerant parser; estimator fallback.
* **User trust (config edits):** backups, diff view, kill‑switch, clean uninstall.
* **Enterprise managed configs:** detect and switch to read‑only mode; document behavior.

## Definition of Done (v1)

Fresh machine → install `.pkg` → start two Claude sessions → **Irrlicht** shows two glyphs with correct states; dropdown lists msgs/min & context %; actions work; uninstall restores original Claude settings exactly.
