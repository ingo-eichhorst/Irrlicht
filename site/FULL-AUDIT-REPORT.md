# Irrlicht SEO Full Audit Report
**Date:** 2026-04-10
**Scope:** irrlicht.io — all pages in site/

---

## SEO Health Score: 61 / 100

| Category | Weight | Score | Weighted |
|---|---|---|---|
| Technical SEO | 25% | 72 | 18.0 |
| Content Quality / E-E-A-T | 25% | 62 | 15.5 |
| On-Page SEO | 20% | 58 | 11.6 |
| Schema / Structured Data | 10% | 58 | 5.8 |
| Performance (Core Web Vitals) | 10% | 48 | 4.8 |
| Images | 5% | 45 | 2.3 |
| AI Search Readiness (GEO) | 5% | 62 | 3.1 |
| **Total** | | | **61 / 100** |

---

## Business Context

- **Site type:** Open-source macOS developer tool (SoftwareApplication)
- **Target audience:** Developers running Claude Code, OpenAI Codex, Pi, or other AI coding agents
- **Deployment:** Static HTML, GitHub Pages
- **Pages crawled:** 16 (index.html, docs/index.html, 13 doc pages, landscape/index.html)
- **Canonical domain:** https://irrlicht.io/

---

## Top 5 Critical Issues

1. **1.7 MB unoptimized PNG hero image** — primary LCP killer. No WebP/AVIF, no preload hint, CSS background discovery is late. Estimated LCP on mobile: Poor.
2. **H1 contains only the brand name "Irrlicht"** — no topic descriptor. Crawler and quality rater cannot identify page topic from the heading hierarchy.
3. **/landscape/ page missing from sitemap.xml** — an indexed, linked page completely absent from the sitemap.
4. **Wrong GitHub URL in installation.html (already fixed)** — `anthropics/irrlicht` was the source, now corrected to `ingo-eichhorst/Irrlicht`.
5. **BreadcrumbList on homepage shows Home > Documentation** — semantically inverted; homepage cannot be its own child.

---

## Top 5 Quick Wins

1. Add `<link rel="preload" as="image" href="assets/bg_no_lights.png">` to `<head>` — 1 line, immediate LCP improvement.
2. Add `/landscape/` entry to `sitemap.xml` — 5-minute edit, immediate crawl coverage.
3. Add `async` to the counter.dev script tag — 6 characters, eliminates synchronous parsing block.
4. Fix BreadcrumbList — change to single-item `[Home]` or remove it from homepage.
5. Add `og:image` meta tag — unlocks social sharing previews across all platforms.

---

## Technical SEO — 72 / 100

### Critical
| # | Issue | File |
|---|---|---|
| T1 | `/landscape/index.html` not in sitemap.xml | site/sitemap.xml |

### High
| # | Issue | File |
|---|---|---|
| T2 | Hero `bg_no_lights.png` (1.7 MB) loaded via CSS with no preload hint | site/index.html |
| T3 | Google Fonts stylesheet is render-blocking on all 16 pages | All pages |
| T4 | No `og:image` or `twitter:image` on any page | All pages |
| T5 | Landscape page (`landscape/index.html`) has no analytics script | site/landscape/index.html |

### Medium
| # | Issue | File |
|---|---|---|
| T6 | `twitter:description` missing from landscape page | site/landscape/index.html |
| T7 | No structured data on docs pages or landscape page | site/docs/*.html, landscape/ |
| T8 | Sitemap has no `<changefreq>` elements; useful for Bing/Yandex | site/sitemap.xml |
| T9 | No IndexNow key or submission mechanism | Deploy pipeline |
| T10 | No custom `404.html` — GitHub Pages shows generic error | site/ |
| T11 | `dmg-background.tiff` (1.0 MB) is served from web root but not referenced in HTML | site/assets/ |

### Low
| # | Issue | File |
|---|---|---|
| T12 | Doc pages use `.html` extension; no clean-URL fallback testing confirmed | site/docs/ |
| T13 | `og:site_name` missing from landscape page | site/landscape/index.html |
| T14 | Per-page BreadcrumbList absent from all doc pages and landscape | All pages |
| T15 | `llms.txt` exists but does not include landscape URL (mirrors sitemap gap) | site/llms.txt |

---

## Content Quality / E-E-A-T — 62 / 100

### E-E-A-T Dimension Scores
| Dimension | Score |
|---|---|
| Experience | 12 / 20 |
| Expertise | 18 / 25 |
| Authoritativeness | 15 / 25 |
| Trustworthiness | 25 / 30 |

### Critical
| # | Issue | File | Line |
|---|---|---|---|
| C1 | H1 contains only brand name — no topic descriptor | site/index.html | 1045 |
| C2 | No visible author attribution on any page | All pages | — |
| C3 | Wrong GitHub org in clone command: `anthropics/irrlicht` **[FIXED]** | site/docs/installation.html | 139 |

### High
| # | Issue | File |
|---|---|---|
| C4 | Homepage body word count ~490-520 words — at the floor, no narrative depth | site/index.html |
| C5 | Zero social proof (no star count, no user count, no testimonials) | site/index.html |
| C6 | Quickstart page is ~220 words, no troubleshooting section | site/docs/quickstart.html |
| C7 | H2 headings contain no primary keywords ("Three states. No ambiguity." etc.) | site/index.html |

### Medium
| # | Issue | File |
|---|---|---|
| C8 | No TechArticle schema on doc pages despite `og:type: article` in OG meta | site/docs/*.html |
| C9 | changelog.html is linked in footer/nav but appears to be empty/stub | site/docs/changelog.html |
| C10 | Feature cards lack "how" mechanism explanations (e.g., Cost Estimation: "Know what you're spending" — no mechanism) | site/index.html |
| C11 | No FAQ / Q&A content block anywhere on the homepage | site/index.html |

### Low
| # | Issue | File |
|---|---|---|
| C12 | No `og:image` — social sharing renders with no preview image | All pages |
| C13 | Twitter card type `summary` instead of `summary_large_image` | All pages |
| C14 | `counter.dev` analytics `data-id` is publicly visible in source | All pages |
| C15 | No `rel="me"` author link in homepage `<head>` | site/index.html |

---

## Schema / Structured Data — 58 / 100

The homepage has 4 JSON-LD blocks (SoftwareApplication, Organization, WebSite, BreadcrumbList). Foundation is good; execution has gaps.

### Critical
| # | Issue | Details |
|---|---|---|
| S1 | BreadcrumbList on homepage is inverted — shows `Home > Documentation` on the homepage itself | Block 4 |
| S2 | URL inconsistency: `https://irrlicht.io` (no trailing slash) vs `https://irrlicht.io/` across Blocks 1, 2, 3 | Blocks 1–3 |

### High
| # | Issue |
|---|---|
| S3 | Zero structured data on all 13 doc pages (no TechArticle, no BreadcrumbList) |
| S4 | No `@id` on Organization, WebSite, or Person — entity linking impossible |

### Medium
| # | Issue |
|---|---|
| S5 | `screenshot` property missing from SoftwareApplication |
| S6 | `datePublished` / `dateModified` absent from SoftwareApplication |
| S7 | `logo` is a bare URL string rather than an `ImageObject` with dimensions |
| S8 | WebSite `publisher` is inline object, not `@id` reference to Organization block |

### Low
| # | Issue |
|---|---|
| S9 | `applicationSubCategory` and `softwareRequirements` missing |
| S10 | `author` Person described twice (SoftwareApplication + Organization) without shared `@id` |

---

## Performance (Core Web Vitals) — 48 / 100

| Metric | Predicted Status | Primary Cause |
|---|---|---|
| LCP | Poor / Needs Improvement | 1.7 MB unpreloaded PNG + render-blocking Fonts |
| CLS | Needs Improvement | Cormorant Garamond FOUT with no `size-adjust` |
| INP | Good | Minimal interactive JS |

### Asset sizes (measured)
| Asset | Size | Used? |
|---|---|---|
| bg_no_lights.png | 1.7 MB | Yes (hero CSS background) |
| bg_lights.png | 1.7 MB | No (unreferenced in index.html) |
| bg_lights_2.png | 1.7 MB | No (unreferenced in index.html) |
| dmg-background.tiff | 1.0 MB | No |
| apple-touch-icon.png | 3.6 KB | Yes |

### Critical
| # | Issue |
|---|---|
| P1 | `bg_no_lights.png` is 1.7 MB PNG, loaded via CSS background with no `<link rel="preload">`. Converting to AVIF expected to yield ~150-300 KB (85-90% reduction). |
| P2 | Google Fonts stylesheet is render-blocking — delays H1 text paint (primary LCP candidate). |

### High
| # | Issue |
|---|---|
| P3 | `counter.dev` script has no `async` or `defer` — synchronous block at end of `<body>`. |
| P4 | CLS from Google Fonts FOUT: Cormorant Garamond is a display serif with unusual metrics; no `size-adjust` or `font-display: optional` compensation. |

### Medium
| # | Issue |
|---|---|
| P5 | Canvas animation loop runs indefinitely with no `visibilitychange` pause. |
| P6 | `bg_lights.png` and `bg_lights_2.png` (1.7 MB each) are in the deployment but unreferenced — remove or convert. |
| P7 | `dmg-background.tiff` (1.0 MB) is a build artifact served from web root. |
| P8 | No `<link rel="preconnect" href="https://cdn.counter.dev">` in `<head>`. |

---

## AI Search Readiness (GEO) — 62 / 100

| Platform | Score |
|---|---|
| Google AI Overviews | 55/100 |
| ChatGPT | 48/100 |
| Perplexity | 65/100 |
| Bing Copilot | 58/100 |

### Critical Signal: Brand Name Conflict
"Irrlicht" is also a well-established C++ 3D rendering engine (irrlicht3d.org) with a Wikipedia article and 20+ years of web presence. AI systems trained on historical data will associate "Irrlicht" primarily with the 3D engine. **Any query containing only "Irrlicht" will likely return the 3D engine, not this tool.**

### llms.txt Assessment
- Present and well-structured: ✓
- Has a citable 95-word summary: ✓
- Links to 9 documentation pages: ✓
- Missing: `llms-full.txt` companion file with full prose content
- Missing: daemon name (`irrlichd`), CLI (`irrlicht-ls`), API shapes, version, macOS requirement
- Missing: landscape page URL (mirrors sitemap gap)
- Missing: `<link rel="alternate" type="text/plain" title="LLMs.txt" href="/llms.txt">` in HTML head

### Citability Gaps
- Hero text is marketing copy, not citable ("In Faust, an Irrlicht guides...")
- No question-format headings anywhere on the site
- No FAQ block
- Feature cards too short for self-contained citation
- Strongest citable line: `Transcript Files → FSEvents/kqueue → SessionDetector → State Machine → WebSocket → Menu Bar` (pipeline-code div)

### robots.txt
- All AI crawlers permitted (wildcard `Allow: /`) ✓
- No explicit named rules for major AI crawlers (GPTBot, ClaudeBot, PerplexityBot)
- No training-opt-out for CCBot/anthropic-ai/cohere-ai if needed

---

## Sitemap Analysis

15 URLs present. Issues:
- `/landscape/` is **missing** (indexed, linked from nav + footer)
- `/llms.txt` not listed (optional but beneficial)
- All `lastmod` dates are `2026-04-04` (consistent, recent)
- No `<changefreq>` on any URL

---

## robots.txt Analysis

```
User-agent: *
Allow: /
Sitemap: https://irrlicht.io/sitemap.xml
```

- No AI crawlers blocked ✓
- No directories blocked ✓  
- Sitemap reference present ✓
- No explicit named AI crawler rules (informational gap, not a problem)

---

## llms.txt Analysis

Present at `https://irrlicht.io/llms.txt`. Well-formed baseline, notable gaps documented above.
