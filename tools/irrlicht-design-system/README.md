# Irrlicht Design System

> *In Goethe's Faust, an Irrlicht guides the way through the night. This one guides you through your agents — who's working, who's waiting, and where you're needed next.*

**Irrlicht** (German: *will-o'-the-wisp*; IPA /eer-likht/) is an open-source macOS menu-bar telemetry app that monitors AI coding agents — Claude Code, OpenAI Codex, Pi, and the Gas Town orchestrator — in real time. Each session is a single colored light:

- 🟣 **working** — the agent is thinking, building, streaming
- 🟠 **waiting** — it needs you; the story pauses for your judgment
- 🟢 **ready** — the path ahead is clear, ready for new work
- **no sessions** — clean slate, dimmed grey flame

The brand is built entirely around that **three-light vocabulary**. Purple, orange, green — everywhere, always in that semantic role.

---

## Surfaces / products

| Product | Tech | What it is |
|---|---|---|
| **Marketing site** (`irrlicht.io`) | Static HTML, Google Fonts | Hero + feature grid + install block. Dark, starry, serif-hero aesthetic. |
| **Docs site** (`irrlicht.io/docs/`) | Static HTML, shared tokens | Left-sidebar docs with callouts, tables, code blocks, state badges. |
| **Web dashboard** (`platforms/web/`) | Vanilla JS, WebSocket | Denser, mono-forward live session list with timeline heatmap and Gas Town panel. |
| **macOS menu-bar app** (`platforms/macos/`) | SwiftUI | The actual product: colored dots in the menu bar + a dropdown list of sessions. |

---

## Sources

- **GitHub:** [ingo-eichhorst/Irrlicht](https://github.com/ingo-eichhorst/Irrlicht) (MIT)
- **Landing page:** https://irrlicht.io/
- **Docs:** https://ingo-eichhorst.github.io/Irrlicht/docs/

Files read directly from the repo at `main@117cf64`:
- `README.md`, `AGENTS.md` — voice, tone, feature narrative
- `site/index.html` — marketing page CSS + structure (the style root)
- `site/docs/docs.css` — docs typography + callouts
- `platforms/web/index.html` — web dashboard, richer semantic colors
- `platforms/macos/Irrlicht/Views/MenuBarStatusRenderer.swift` — exact SVG rendering of menu-bar dots

---

## Index — what lives in this folder

```
README.md                   ← you are here
SKILL.md                    ← Claude Code-compatible skill manifest
colors_and_type.css         ← CSS custom properties for the whole system

assets/
  logo.png                  ← Hero/banner logo (1.3MB, dark bg)
  banner.png                ← README banner (1.3MB)
  irrlicht-explainer.png    ← 3-light UI explainer screenshot
  bg_lights.png             ← Starry night bg with wisps (full-bleed)
  bg_no_lights.png          ← Starry night bg — clean (hero)
  favicon.svg               ← The wisp flame mark (vector, on-brand)
  favicon-16x16.png · favicon-32x32.png · apple-touch-icon.png

preview/                    ← Design-system cards (see Design System tab)
  type-*.html               · typography specimens
  color-*.html              · palette swatches
  spacing-*.html            · radii, shadows, tokens
  component-*.html          · buttons, badges, cards, inputs
  brand-*.html              · logos, wisp mark, backgrounds

ui_kits/
  landing/                  ← Marketing-site UI kit (hero, three lights, install)
  dashboard/                ← Web dashboard UI kit (sessions, Gas Town, alerts)
```

---

## Content fundamentals

Irrlicht writes like a quietly confident German open-source maintainer. Copy is **direct, literary in moments, technical when it counts, and never fluffy**. It trusts the reader.

### Voice

- **Second-person, active, present-tense.** *"You don't know which session needs you."* / *"Install, run, and Irrlicht discovers your sessions automatically."*
- **Problem-led, not feature-led.** Marketing copy names the pain first (*"Parallel sessions shred your attention"*), then the fix.
- **Receipts.** Technical claims carry citations — GitHub issues, dev.to, The Register. No vague *"studies show."*
- **Literary openings, mechanical endings.** A block often opens with the Faust line or an italic aside, then drops into a deterministic `Transcript Files → FSEvents → State Machine → Menu Bar` diagram.

### Tone & vibe

- **Calm, ambient, occult-adjacent.** The product *is* a will-o'-the-wisp — light, guidance, night, lanterns. Leans into that without becoming mystical.
- **No hype, no "game-changing," no rocketship emoji.** If the docs call something "zero configuration" it's because there's nothing to configure, full stop.
- **Slight dry humour.** *"Follow the right light."* *"Three states. No ambiguity."* *"In the dark"* (cost tracking).

### Casing & punctuation

- **Sentence case** for headings (`How it works`, `The light system`), never Title Case.
- State names are **always lowercase**: `working`, `waiting`, `ready`, `cancelled`. Even in UI badges.
- Em-dashes (—) are load-bearing. Curly quotes ("…") throughout. `&mdash;` in HTML.
- Numbers: compact (`<1s`, `~5MB`, `80%`, `$0.42`) with no thin-space padding.
- Code in backticks mid-sentence: *"watches `.jsonl` transcripts."*

### Signature phrases

- "Follow the right light." *(footer / tagline)*
- "Menu-bar telemetry for AI coding agents" *(header subtitle)*
- "Three states. No ambiguity."
- "Ambient, always visible, nothing to click through."
- "Local-first, ~5MB RAM, no telemetry leaves your machine."
- "Zero configuration. No hooks. No SDK."

### Emoji & unicode

- **Emoji used sparingly and always semantically.** Only the four light glyphs: 🟣 🟠 🟢 ✦. Occasionally ⚡ ⛽ for Gas Town, 🚚 for Convoys. Never decorative.
- **Unicode-as-icon is common.** `→ ↵ ⌘ ✦ ● ○ ✓ ⚠` appear inline in code blocks and pipelines. The mono font carries them cleanly.
- **No emoji in headings.** Keep section titles text-only.

### Examples (copy directly from this palette)

| Situation | Do | Don't |
|---|---|---|
| Feature headline | *"Real-time state detection"* | *"🚀 Lightning-fast monitoring!"* |
| Status update | *"3 agents busy."* *"One needs you."* | *"You have 3 active sessions currently working."* |
| Install prose | *"Up and running in sixty seconds."* | *"Get started in seconds with our easy install."* |
| Problem framing | *"Cost runs away in the dark."* | *"Track your spending."* |

---

## Visual foundations

Dark, starry, deliberately **off the usual SaaS trail**. Think: telescope observatory + Apple menu bar + a touch of old-book serif.

### Colors

Three semantic lights, on near-black navy. **Never invent new accent colors** — everything non-state leans on the purple or a neutral grey.

- `--working:   #8B5CF6` · violet 500-ish
- `--waiting:   #FF9500` · iOS orange
- `--ready:     #34C759` · iOS green
- `--bg:        #050a14` · near-black navy (deepest)
- `--bg-surface:#0a1020` · raised panels
- `--text:      #c8cdd8` · cool off-white body
- `--text-dim:  #5a6378` · label/meta

Semantic pressure scale adds: `#FF3B30` (high) and `#D70015` (critical) — **only for context-pressure UI**, not general error states.

### Type

Three-family system. Always use in their assigned role — no cross-casting.

1. **Cormorant Garamond** — serif display. Light weight 300. Used for: logo wordmark, hero H1, big section titles ("Three states."), italic meanings. Never for body.
2. **Outfit** — humanist sans. Weights 200–600. Used for: body, subheads, buttons, feature copy.
3. **DM Mono** — monospace. Weights 300/400/500. Used for: eyebrows, code, data, small uppercase labels, metrics, CLI. **This is the brand's workhorse** — more than half the interface.

Letter-spacing matters: mono eyebrows are tracked 0.2–0.3em; the serif hero is tracked 0.08em; monospace IRRLICHT logo is tracked 0.25em.

### Backgrounds

- **Near-black navy** (`#050a14`) everywhere; never pure `#000`.
- **Two photographic PNG backgrounds** (`bg_lights.png`, `bg_no_lights.png`): starry nights with a forest silhouette. Used full-bleed behind the hero.
- **Layered overlays** are the look:
  1. Noise SVG overlay at `opacity:0.025` — subtle film grain, `position:fixed`.
  2. Faint 60px grid pattern at `opacity:0.03` (web dashboard only).
  3. Radial glow ellipse from bg-glow to bg for page-level depth.
- **No gradients on backgrounds.** Ever. The only gradient usages: the wordmark text, primary button fill, and a single hairline at the top of glass cards.

### Animation & motion

- **Easing is always `cubic-bezier(0.16, 1, 0.3, 1)`** — a gentle "decelerate-and-settle."
- **Breathing / pulse loops** on the three orbs: 2.5–3.5s, scale 1→1.2, opacity 1→0.7. Staggered per color so the three hero lights are never in phase.
- **Scroll-reveals**: 20–30px translateY + opacity fade, 0.8s, staggered 80ms per child.
- **No bounces. No springs. No parallax.** The mood is candlelight, not carnival.
- Canvas wisps: a single floating radial-gradient blob, fades in/out over ~3–6 seconds, cycles through purple → orange → green.

### Hover & press states

- **Nav/text links:** color dims → brightens on hover (`--text-dim` → `--text-bright`). 0.3s.
- **Feature cards:** `translateY(-4px)`, purple-tinted border, purple halo shadow.
- **Primary buttons:** `translateY(-2px)` and stronger glow. No fill change.
- **Secondary buttons:** border brightens from `rgba(200,205,216,0.15)` → `0.30`, bg lifts to `0.03` white.
- **Install-cmd copy button:** bg fills with `--working-dim`, then flashes `--ready` (green "Copied") for 1.5s.
- **No scale-down press states.** No ripples.

### Borders & hairlines

- Near-universal border: `1px solid rgba(138, 92, 246, 0.08)` — a near-invisible purple hairline that reads as *structure* more than *edge*.
- Web dashboard uses `#1a2340` borders — slightly more opaque because the UI is denser.
- **Top-edge accent lines**: `linear-gradient(90deg, transparent, rgba(139,92,246,0.2), transparent)` as a `::before` on cards. This is the single most-used decorative flourish.

### Shadows & glow

Two systems, used separately:

1. **Elevation shadow** (cards at rest): `0 20px 60px rgba(0,0,0,0.30)` on hover only.
2. **Colored glow** (lights, active buttons): the three-light halo — e.g., `0 0 20px #8B5CF6, 0 0 60px rgba(139,92,246,0.3)`. Applied to dots, orbs, the primary CTA.

No inner shadows. No neumorphism. No frosted "gummy" looks beyond one 12–20px `backdrop-filter: blur(…)` on the nav/cards.

### Radii

- `3–5px` tiny (badges, inline code)
- `6px` web dashboard cards (denser product feel)
- `10px` code blocks, install inputs
- `16px` marketing feature cards (softer, calmer)
- `24px` the big OSS CTA card

### Cards

Glass cards are the default:
```
background: rgba(12, 18, 35, 0.65);
border: 1px solid rgba(138, 92, 246, 0.08);
backdrop-filter: blur(12px);
border-radius: 16px;
padding: 2rem;
```

Plus the `::before` top hairline gradient described above. On hover, add the purple-tinted translucent border and the elevation shadow.

### Layout rules

- **Max content width 1120px** (marketing) / **780px** (docs) / **1200px** (dashboard).
- Fixed nav at top with `backdrop-filter: blur(20px) saturate(1.2)` over a 70% opaque dark wash.
- Grids: mostly 3-column at desktop, collapsing to 2 at ≤900px, 1 at ≤600px.
- Pipeline / process rows: flex with chevron connectors (`border-left: 10px solid rgba(139,92,246,0.2)` as ::after triangles).
- Generous vertical rhythm: sections are 8rem top/bottom padding. The brand *breathes*.

### Transparency & blur

- Navs: 70% opaque over `blur(20px) saturate(1.2)`.
- Cards: 65% opaque (`--bg-card`) over `blur(8–12px)`.
- **Blur is almost always combined with low alpha**, never on top of pure colors.

### Imagery vibe

- **Cold, starry, high-contrast.** Warm candlelight accents (orange wisp) against cool navy.
- Faint film grain present everywhere via the noise overlay.
- Screenshots are rendered dark-mode with the brand palette intact — we never show a light-mode mock.

---

## Iconography

Irrlicht is **deliberately icon-light**. Most of what looks like iconography is actually **colored dots** or **unicode glyphs** in the mono font.

### 1. The Light System (primary visual vocabulary)

- 🟣 🟠 🟢 — colored dots (SVG or `<span>` with border-radius:50%) in the three light colors, plus a dimmed grey flame for "no sessions" (see `assets/favicon-off.svg`).
- These are the brand. They appear in the menu bar, web dashboard header, session rows, marketing hero, and the favicon.
- In the macOS menu bar, groups of ≤3 sessions overlap as dots; ≥4 become a pie chart with a number label — see `MenuBarStatusRenderer.swift`.

### 2. Inline SVG for feature cards

Six small outline SVGs appear on the marketing feature grid (real-time monitoring, context pressure, cost, multi-agent, git-aware, zero-config). They are:

- **20×20 viewBox**, `fill: none`, `stroke-width: 1.5`, `stroke-linecap: round`.
- Coloured with `currentColor` inside purple/orange/green 40×40 rounded squares at 10% opacity.
- **Not a library.** These are hand-authored, tuned for the dark bg. If new feature icons are needed, draw to this same spec (1.5 stroke, rounded, 20×20).

### 3. Unicode glyphs (very common)

- `→ ←` arrows (`&rarr;`, `&larr;`) in pipelines and CTAs.
- `● ○` filled/hollow circles for convoy dot-bars in the dashboard.
- `✓` checkmark (`&#10003;`) for completed items.
- `⚠` warning triangle for pressure alerts.
- `▾ ▸` (`&#9662;`) chevrons for collapsible groups.
- `⌘` for keyboard shortcuts.
- `⛽ 🚚` for Gas Town / Convoys specifically.

### 4. State icons in the web dashboard

Three small 12×12 SVGs live inline in `platforms/web/index.html` (see `svgIcons` object):
- **working**: dashed-stroke circle, spinning.
- **waiting**: two vertical bars (pause).
- **ready**: circle + checkmark.
- **cancelled**: circle + X.

Copy these verbatim for any session-row UI.

### 5. Substitutions & gaps

- **No icon font is used.** No Lucide, no Heroicons, no Font Awesome in the source.
- For generic utility icons outside the 6 feature cards (e.g. a settings gear, a clipboard), **prefer unicode first**, then fall back to inline SVGs matching the 1.5-stroke / 20×20 spec. **Do not introduce Lucide or Heroicons** — it would dilute the visual language.

### 6. The wisp / flame mark

- `assets/favicon.svg` is the primary brand mark — a stylized flame silhouette in `#8B5CF6` with a `#c4b5fd` inner glow and a radial-gradient halo. Scale it up for logos.
- The word IRRLICHT is paired with a small breathing purple dot (`.sparkle`) to the left in the nav.

---

## Fonts

All three families are Google Fonts — no local files bundled. Import in page `<head>`:

```html
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Cormorant+Garamond:ital,wght@0,300;0,400;0,600;1,300;1,400&family=DM+Mono:wght@300;400;500&family=Outfit:wght@200;300;400;500;600&display=swap" rel="stylesheet">
```

No substitutions needed — the design references these exact families. If building a Claude Code skill offline, the CSS falls back to `serif` / `system-ui` / `ui-monospace` gracefully.

---

## Quick usage

1. Include `colors_and_type.css` at the top of any new HTML.
2. Near-black navy `--bg`. Purple for state/accents, orange/green only where semantics demand it.
3. Cormorant Garamond light 300 for hero type. DM Mono for *all small UI labels*. Outfit for everything else.
4. One faint purple hairline border per card. One top-edge gradient `::before`. Breathing dots where a session is alive.
5. Never use gradients on backgrounds. Never bounce. Lowercase state names.

Follow the right light.
