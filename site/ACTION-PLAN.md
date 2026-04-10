# Irrlicht SEO Action Plan
**Date:** 2026-04-10
**Overall Score:** 61 / 100

---

## Critical (Fix Immediately)

### AC1 — Convert hero PNG to WebP/AVIF and add preload hint
**File:** `site/index.html`, `site/assets/`
**Impact:** LCP — moves from Poor to Good on most devices

`bg_no_lights.png` is 1.7 MB. Convert to AVIF (expected ~150-300 KB) and add a preload hint before the Google Fonts links:

```html
<!-- Add to <head>, before Google Fonts links -->
<link rel="preload" as="image" href="assets/bg_no_lights.avif" type="image/avif">
```

Update the CSS background line (~L264):
```css
/* Use image-set for AVIF with PNG fallback */
background: url('assets/bg_no_lights.png') center center / cover no-repeat;
```
To:
```css
background: image-set(
  url('assets/bg_no_lights.avif') type("image/avif"),
  url('assets/bg_no_lights.png') type("image/png")
) center center / cover no-repeat;
```

Also remove/convert `bg_lights.png`, `bg_lights_2.png` (1.7 MB each, unreferenced) and `dmg-background.tiff` (1.0 MB, build artifact) from the deployment.

---

### AC2 — Fix BreadcrumbList on homepage (inverted hierarchy)
**File:** `site/index.html` (JSON-LD Block 4)
**Impact:** Schema — eliminates Google Search Console rich result error

Replace the 2-item `Home > Documentation` breadcrumb with a single-item list (or remove it entirely):

```json
{
  "@context": "https://schema.org",
  "@type": "BreadcrumbList",
  "itemListElement": [
    {
      "@type": "ListItem",
      "position": 1,
      "name": "Home",
      "item": "https://irrlicht.io/"
    }
  ]
}
```

---

### AC3 — Add /landscape/ to sitemap.xml
**File:** `site/sitemap.xml`
**Impact:** Crawlability — ensures Googlebot discovers and indexes the landscape page

Add before `</urlset>`:
```xml
<url>
  <loc>https://irrlicht.io/landscape/</loc>
  <lastmod>2026-04-10</lastmod>
  <priority>0.7</priority>
</url>
```

Also add `/landscape/` to `site/llms.txt` under the Documentation section.

---

## High Priority (Fix Within One Week)

### AH1 — Make Google Fonts non-render-blocking
**File:** All 16 HTML pages
**Impact:** LCP — eliminates font stylesheet as a render-blocking resource

Replace (on every page):
```html
<link href="https://fonts.googleapis.com/css2?..." rel="stylesheet">
```
With:
```html
<link href="https://fonts.googleapis.com/css2?..." rel="stylesheet" media="print" onload="this.media='all'">
<noscript><link href="https://fonts.googleapis.com/css2?..." rel="stylesheet"></noscript>
```

---

### AH2 — Add og:image and upgrade Twitter card
**File:** All pages
**Impact:** Social sharing — enables image previews on all social platforms

Create a 1200×630px `og-image.png` in `site/assets/`. Add to `<head>` on every page:
```html
<meta property="og:image" content="https://irrlicht.io/assets/og-image.png">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:image" content="https://irrlicht.io/assets/og-image.png">
```

---

### AH3 — Fix H1 to include topic descriptor
**File:** `site/index.html` (line ~1045)
**Impact:** On-Page SEO, E-E-A-T — H1 currently says only "Irrlicht"

Change the H1 text or add a subtitle H2 directly below it. The gradient styling can remain on the brand name. Example approach — change the hero subtitle-top `<p>` to an `<h2>`:

```html
<h1><span>Irrlicht</span></h1>
<h2 class="hero-subtitle-top">Menu-Bar Telemetry for AI Coding Agents</h2>
```

(Update the CSS class `.hero-subtitle-top` to target both `p` and `h2` if needed.)

---

### AH4 — Add analytics to landscape page
**File:** `site/landscape/index.html`
**Impact:** Measurement — traffic to this page is currently invisible

Copy the counter.dev script tag from index.html and add it before `</body>` in landscape/index.html. The `data-id` should match.

---

### AH5 — Make counter.dev script async on all pages
**File:** All pages with the script
**Impact:** Performance — eliminates synchronous script block at end of `<body>`

Change:
```html
<script src="https://cdn.counter.dev/script.js" data-id="..." data-utcoffset="1"></script>
```
To:
```html
<script async src="https://cdn.counter.dev/script.js" data-id="..." data-utcoffset="1"></script>
```

Also add `<link rel="preconnect" href="https://cdn.counter.dev">` to `<head>` on all pages.

---

### AH6 — Fix URL inconsistency in schema (trailing slash)
**File:** `site/index.html` (JSON-LD Blocks 1, 2, 3)
**Impact:** Schema — resolves entity mismatch with canonical URL

In all three blocks, change `"https://irrlicht.io"` → `"https://irrlicht.io/"` (add trailing slash) to match the canonical tag. Affects `url`, `@id`, and `publisher.url` fields.

---

### AH7 — Add @id to all schema entities and link them
**File:** `site/index.html` (all 4 JSON-LD blocks)
**Impact:** Schema — enables entity graph linking across blocks

Add `"@id"` to each block:
- Organization: `"@id": "https://irrlicht.io/#organization"`
- WebSite: `"@id": "https://irrlicht.io/#website"`
- SoftwareApplication: `"@id": "https://irrlicht.io/#software"`
- Person (author/founder): `"@id": "https://github.com/ingo-eichhorst"`

Then replace the inline `publisher` object in the WebSite block with: `"publisher": {"@id": "https://irrlicht.io/#organization"}`

---

### AH8 — Add visible author attribution
**File:** `site/index.html` footer (line ~1363)
**Impact:** E-E-A-T Trustworthiness — author currently invisible to human readers

Change the footer left text from:
```
MIT License · Irrlicht v0.3.2 · Follow the right light.
```
To:
```
MIT License · Irrlicht v0.3.2 · Built by <a href="https://github.com/ingo-eichhorst">Ingo Eichhorst</a>
```

---

## Medium Priority (Fix Within One Month)

### AM1 — Add structured data to all docs pages
**File:** `site/docs/*.html` (13 pages)
**Impact:** Rich results, AI citation readiness

Each docs page needs two JSON-LD blocks: a `TechArticle` and a `BreadcrumbList`. Template per page:

```json
{
  "@context": "https://schema.org",
  "@type": "TechArticle",
  "headline": "[Page Title] — Irrlicht Docs",
  "description": "[Meta description text]",
  "url": "https://irrlicht.io/docs/[page].html",
  "datePublished": "2026-04-04",
  "dateModified": "2026-04-04",
  "author": {"@type": "Person", "@id": "https://github.com/ingo-eichhorst", "name": "Ingo Eichhorst"},
  "publisher": {"@id": "https://irrlicht.io/#organization"},
  "isPartOf": {"@id": "https://irrlicht.io/#website"},
  "about": {"@id": "https://irrlicht.io/#software"},
  "proficiencyLevel": "Beginner"
}
```

BreadcrumbList for each: `Home > Documentation > [Page Name]`

---

### AM2 — Add FAQ block to homepage
**File:** `site/index.html`
**Impact:** AI citability, Google AI Overviews, long-tail query coverage

Add before the footer, a FAQ section with `FAQPage` JSON-LD and H3 question headings:

Suggested questions:
- "What is Irrlicht?" (explicitly disambiguate from Irrlicht 3D engine)
- "Which AI coding agents does Irrlicht support?"
- "How does Irrlicht detect agent state?"
- "Does Irrlicht send data to the cloud?"
- "What does each light color mean?"
- "Is Irrlicht free?"

---

### AM3 — Rewrite homepage H2 headings to include keywords
**File:** `site/index.html`
**Impact:** On-page SEO, keyword signals in heading outline

Current → Proposed:
- "Three states. No ambiguity." → "Three Agent States, Zero Ambiguity"
- "Everything you need, nothing you don't" → "Real-Time AI Agent Monitoring Features"
- "What Irrlicht works with" → "Supported AI Agents and Platforms"
- "Up and running in sixty seconds" → "Install Irrlicht in Sixty Seconds"

(Keep marketing phrasing in the `<p class="section-desc">` copy below each heading.)

---

### AM4 — Create llms-full.txt
**File:** `site/llms-full.txt` (new file)
**Impact:** AI citability — enables LLMs to answer detailed questions without 9+ fetches

Compile the full prose content from each documentation page into a single Markdown file. Reference it from `llms.txt` with a comment line. Add it to `sitemap.xml`.

---

### AM5 — Disambiguate Irrlicht from the 3D engine
**File:** `site/llms.txt`, `site/index.html` (FAQ), `site/index.html` (meta description update)
**Impact:** AI search — critical for ChatGPT/Perplexity where the 3D engine dominates training data

Add to `llms.txt` (after the blockquote):
```
Note: Irrlicht (this macOS menu bar app) is unrelated to the Irrlicht 3D rendering engine (irrlicht3d.org). This tool monitors AI coding agent sessions; the name refers to the will-o'-the-wisp (Irrlicht) from Goethe's Faust.
```

Include the same disambiguation in the FAQ answer for "What is Irrlicht?".

---

### AM6 — Add `twitter:description` to landscape page
**File:** `site/landscape/index.html`
**Impact:** Social — Twitter card renders incomplete without it

```html
<meta name="twitter:description" content="[same as og:description on that page]">
```

---

### AM7 — Add og:site_name to landscape page
**File:** `site/landscape/index.html`
**Impact:** Consistency — all other pages have this tag

```html
<meta property="og:site_name" content="Irrlicht">
```

---

### AM8 — Add missing SoftwareApplication schema properties
**File:** `site/index.html` (JSON-LD Block 1)
**Impact:** Schema — fills recommended Google rich result fields

Add to the SoftwareApplication block:
```json
"screenshot": "https://irrlicht.io/assets/og-image.png",
"datePublished": "2026-04-07",
"dateModified": "2026-04-07",
"applicationSubCategory": "Developer Tools",
"softwareRequirements": "macOS 13 Ventura or later"
```

---

### AM9 — Expand quickstart.html with troubleshooting
**File:** `site/docs/quickstart.html`
**Impact:** Content depth, E-E-A-T, user satisfaction

Add at minimum:
- H2: "Troubleshooting" — Gatekeeper approval, daemon not starting, no sessions appearing
- H2: "Understanding Context Pressure" — explain what the percentage means
- Cross-reference to installation.html for Gatekeeper step

---

### AM10 — Populate changelog.html
**File:** `site/docs/changelog.html`
**Impact:** Freshness signal, trustworthiness, version history

Add at minimum v0.3.2 and v0.3.1 release notes. A visible changelog is a strong trustworthiness signal for developer tools.

---

## Low Priority (Backlog)

### AL1 — Add IndexNow to deploy pipeline
Generate an IndexNow key, place `<key>.txt` at site root, and fire a POST to `https://api.indexnow.org/indexnow` on each GitHub Pages deploy. Near-instant indexing on Bing.

### AL2 — Add custom 404.html
Create `site/404.html` with site navigation and links back to homepage and docs hub.

### AL3 — Add explicit AI crawler rules to robots.txt
Add named `User-agent: GPTBot`, `ClaudeBot`, `PerplexityBot` blocks before the catch-all to signal deliberate AI-readiness.

### AL4 — Add `<link rel="alternate">` for llms.txt
In `<head>` of index.html:
```html
<link rel="alternate" type="text/plain" title="LLMs.txt" href="/llms.txt">
```

### AL5 — Add `rel="me"` author link
In `<head>` of index.html:
```html
<link rel="me" href="https://github.com/ingo-eichhorst">
```

### AL6 — Add `<changefreq>` to sitemap
Add `<changefreq>weekly</changefreq>` to homepage and landscape, `<changefreq>monthly</changefreq>` to stable docs.

### AL7 — Pause canvas animation on tab hidden
In the wisp canvas IIFE, add:
```js
let rafId;
document.addEventListener('visibilitychange', () => {
  if (document.hidden) cancelAnimationFrame(rafId);
  else rafId = requestAnimationFrame(animate);
});
// Replace requestAnimationFrame(animate) calls with: rafId = requestAnimationFrame(animate)
```

### AL8 — Create a YouTube demo video
A 2-3 min screen recording titled "Irrlicht — macOS Menu Bar Monitor for Claude Code and AI Agents". YouTube mentions correlate ~0.737 with AI citation frequency (strongest known GEO signal).

### AL9 — Expand sameAs in Organization schema
Once any of these exist, add to `sameAs`: Product Hunt listing, AlternativeTo page, Homebrew Cask entry, Hacker News launch post.

---

## Bugs Fixed in This Audit

| File | Line | Issue | Fix Applied |
|---|---|---|---|
| `site/docs/installation.html` | 139 | `git clone` URL used `anthropics/irrlicht` (wrong org) | Changed to `ingo-eichhorst/Irrlicht` |

---

## Score Projection After Fixes

| Category | Current | After Critical+High | After All |
|---|---|---|---|
| Technical SEO | 72 | 88 | 93 |
| Content Quality | 62 | 74 | 82 |
| On-Page SEO | 58 | 74 | 81 |
| Schema | 58 | 76 | 85 |
| Performance | 48 | 72 | 78 |
| Images | 45 | 80 | 84 |
| AI Search | 62 | 72 | 82 |
| **Overall** | **61** | **79** | **85** |
