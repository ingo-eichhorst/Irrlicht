# Release notes — three-layer template

This template is the source of truth for what a release ships *to readers*.
It produces three artifacts from one draft:

- **GitHub release body** — Headline + Highlights + Also + collapsed appendix.
- **`CHANGELOG.md`** — full three layers, plain Markdown.
- **`site/docs/changelog.html`** — full three layers, with images inlined.

The point is **hierarchy**. Past releases packed every change — a marquee
quota feature and a 63-entry alias-map fix — into one flat bulleted wall.
Readers couldn't tell what was a headline shift from what was a paper cut.
This template forces a small set of explicit user-facing Highlights to the
top, demotes everything else to a flat "Also" list, and tucks the dense
implementation context into an appendix that's still in the same document
for anyone who wants it.

References for the format choice: layered release notes
(AnnounceKit / monday.com / Featurebase 2026 guides) and small-developer-tool
practice (Raycast's per-release Highlights + screenshot pattern).

---

## Structure

```
## <one-sentence headline>

## Highlights

### <Feature name>
<screenshot or GIF>
<2 lines: what changed for the user>
**Why it matters:** <one sentence>
<PR / issue link>

### <…second highlight…>
### <…third highlight…>     (≤ 3 total)

## Also in this release

**Added**
- <one-liner> (#PR)
- <one-liner> (#PR)

**Fixed**
- <one-liner> (#PR)

**Changed / Docs / Distribution**
- <one-liner> (#PR)

## Technical appendix

<dense per-feature bullets with implementation context, file paths,
edge cases — the current single-bullet-per-change format>
```

---

## Selecting Highlights — the load-bearing step

Before drafting any prose, list every change in the release as a one-liner
and run each through these gates. A change is a **Highlight** only if it
passes all three:

1. **User-visible?** A non-developer end user would notice the difference
   in the app, the dashboard, the menu bar, or a notification. (A
   parser-internal fix is not user-visible even if it changes behavior in
   edge cases.)
2. **Screenshot-able?** There exists, or you can produce, an image or GIF
   that shows the change. If a change can't be shown, it can't be a
   Highlight — the reader has nothing to anchor on.
3. **Distinct from recent Highlights?** Not a refinement of something
   highlighted in the last 1–2 releases. The "we shipped X again" story
   reads as noise.

**Cap at three.** Four+ Highlights dilutes back into a flat list. If a
release has more than three candidates, demote the weakest to Also.

If a release has **zero** Highlights (a patch release with no
user-visible-and-screenshot-able change), skip the Highlights section
entirely. Headline + Also + Appendix.

---

## Writing each Highlight

**Two lines of plain-language summary** — write for a user who knows
Irrlicht exists and uses it daily, but doesn't read CHANGELOGs.
- Bad: "Adds `core/pkg/capacity/aliases.go` (a 63-entry alias map ported
  from codeburn's `BUILTIN_ALIASES`) and resolves it inside
  `GetModelCapacity` before the existing lookup."
- Good: "Sessions running through Cursor, OMP, or Antigravity now show
  real dollar costs instead of $0. These frontends rewrite the model
  name before the LLM call, which used to defeat the pricing lookup."

**Screenshot** — `assets/releases/v<version>/<slug>.png`. WebP under
`site/assets/releases/v<version>/<slug>.webp` for site rendering. If the
change has a *before/after* (a brand refresh, an icon overhaul, a layout
shift), produce a single side-by-side image rather than two stacked
images — readers shouldn't have to scroll between them.

**Why it matters** — one sentence on the user benefit, not the
implementation cleverness. "Subscription users can now see when they'll
hit their 5-hour or weekly quota *before* a hard stop" beats "linear
projection over a 5-sample rolling history".

**Link** — `(#PR, #issue)` at the end. Readers who want the full
implementation context jump to the appendix or the PR.

---

## Also in this release

Flat bullets, grouped by Keep-a-Changelog category (**Added** /
**Fixed** / **Changed** / **Docs** / **Distribution** / **Security**).

**One line per item, no more.** If a change needs two lines, it's
probably a Highlight or it belongs in the appendix only. Examples that
fit this section cleanly:

- **Codex: detect `<proposed_plan>` as user-blocking** (#322) — same
  semantic as Claude Code's `ExitPlanMode`, now flips sessions to
  waiting instead of ready.
- **kitty: click-to-focus lands on the right window and tab** (#326) —
  fixes three failures in the kitty focus path.
- **Adapters honor agent-CLI env vars for relocated session dirs** (#349) —
  Pi, Claude Code, Codex now respect their upstream conventions.

If a one-liner can't convey what changed, the appendix carries the
detail. Don't expand the one-liner into a paragraph.

---

## Technical appendix

This is the current dense format, **kept verbatim** as the third layer.
One bullet per change, written for contributors / replay-fixture
maintainers / future-you debugging a regression. Include implementation
paths, edge cases, why the fix is shaped the way it is, fixture impact.

The appendix exists so the layered format doesn't *lose* the technical
context — it just stops putting it in the first thing a reader sees.

Wrap the appendix in `<details><summary>Technical detail</summary>` on
the GitHub release body so it collapses by default. In `CHANGELOG.md`
and `site/docs/changelog.html` it renders inline (the audience for those
surfaces is more technical and isn't scrolling past it).

---

## Worked example — what v0.4.5 looks like in this template

Reformatted version of the v0.4.5 release, for reference. Do **not**
retroactively edit v0.4.5 — this is just the model the next release
should copy from.

```markdown
## v0.4.5 — Subscription quota lands in the menu bar; brand refresh across the app

### Highlights

#### Pro / Max quota forecast in the macOS overlay
![Quota progress bars in the menu-bar header](../../assets/releases/v0.4.5/quota-forecast.png)

The menu-bar header now shows a stacked 5-hour and weekly progress bar for
Anthropic Pro / Max and OpenAI ChatGPT subscriptions, with a linear-projection
forecast of when you'll hit the quota.

**Why it matters:** subscription users get a heads-up *before* a hard stop,
not after — and can see at a glance whether a long-running session is on
track to burn the week.

(#309, #379)

#### New gradient flame across the brand
![Old wisp on the left, new single-path silhouette on the right](../../assets/releases/v0.4.5/flame-before-after.png)

The flame mark is redrawn as a single-path silhouette with per-state
gradients (purple working / orange waiting / green ready), plus flat
mono and black/white variants. App icon, menu bar, landing-page navbar,
and design-system previews all swap in the new mark.

**Why it matters:** a cleaner silhouette reads better at 22px in the menu
bar and at 1024px on the icon. The state colors now match the rest of
the app's state vocabulary.

(#388)

#### Unified state icons across web and macOS
![Heartbeat halo, pause bars, checkmark — web and macOS side by side](../../assets/releases/v0.4.5/state-icons.png)

`working` is now a heartbeat halo (purple), `waiting` is two pause bars
(orange), `ready` stays a checkmark. Same vocabulary in the web dashboard
and the macOS overlay — no more dashed circle in one place and an
hourglass in the other.

**Why it matters:** if you watch both surfaces (laptop screen + menu
bar), state is now legible from either without re-learning.

(#382, closes #380)

### Also in this release

**Added**
- **`/ir:triage` assigns release milestones on `ready-for-agent`** (#375) — triaged issues land directly in `v0.5` / `v0.6` / `v0.7` based on priority, no manual pass needed.

**Fixed**
- **Detect imperative waiting cues without a literal `?`** (#381, #383) — "let me know if it's right" / "verify and reply" now flip the session to waiting.
- **Web: migrate sessions when `project_name` arrives after first push** (#377) — agents no longer get stuck in the "unknown" group when git metadata is slow.
- **Pricing: normalize frontend-rewritten model names** (#371, #376) — Cursor / OMP / Antigravity sessions price at non-zero (63-entry alias map synced from codeburn).

**Docs**
- **List VS Code extension as a planned platform** (#370).

### Technical appendix

<details><summary>Implementation detail</summary>

[full current-format bullets here — verbatim from what v0.4.5 shipped with]

</details>
```

The contrast: v0.4.5 as shipped opens with one 600-word bullet on the
quota feature, immediately followed by a 500-word bullet on the brand
refresh, immediately followed by a 400-word bullet on state icons. There
is no place for a reader's eye to land. The reformatted version surfaces
three Highlights with screenshots in the first two screens of the page,
then lets anyone who wants the technical depth click through.

---

## Asset paths

```
assets/releases/v<version>/<slug>.png        ← source / CHANGELOG.md
site/assets/releases/v<version>/<slug>.webp  ← site rendering (smaller)
site/assets/releases/v<version>/<slug>.png   ← fallback for site
```

`<slug>` is kebab-case from the Highlight name (`quota-forecast`,
`flame-before-after`, `state-icons`).

For **before/after** images, produce one side-by-side composite rather
than two separate files. Composite filename: `<slug>-before-after.png`.

---

## When *not* to use this template

- Hotfix-only releases with no user-visible change (e.g. cask sha-only
  re-bumps): just the appendix is fine; skip Highlights and Also.
- Release-tool / CI-only releases: appendix only.
- Pre-1.0 versions where the headline shift *is* internal architecture
  (Phase A refactor in v0.4.0): a single Highlight describing the
  contributor-visible change is reasonable; cap at one.
