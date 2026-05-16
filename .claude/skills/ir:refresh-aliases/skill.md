---
name: "ir:refresh-aliases"
description: "Sync irrlicht's model-name alias map against codeburn's upstream BUILTIN_ALIASES. Fetches codeburn's src/models.ts, diffs entries against core/pkg/capacity/aliases.go, and proposes additions/changes as a PR. Use when user says '/ir:refresh-aliases', 'refresh aliases', 'sync codeburn aliases', 'check alias map', or when a session prices at $0 for a known model."
---

# Refresh Model Aliases from Codeburn

Irrlicht's `core/pkg/capacity/aliases.go` is a hand-translated port of codeburn's `BUILTIN_ALIASES` map. New frontends (Cursor releases, OMP variants, Antigravity Gemini models, etc.) appear upstream first; this skill keeps our map in sync.

## Modes

This skill runs in two modes — the workflow below covers both; only the apply step (Step 7) differs.

- **Standalone** (default — invoked as `/ir:refresh-aliases` or "refresh aliases"): produce a diff report, edit `aliases.go`, run tests, and open a PR. Maintainer reviews the PR before it lands on `main`.
- **Release-inline** (invoked from `/ir:release` Step 1.5): produce the same diff, but edit `aliases.go` directly on the active release branch — no separate PR. The release commit ships the alias updates alongside the version bump. **Fail-soft**: if the fetch errors out (offline, upstream 5xx), continue the release with the existing map.

## When to run (standalone)

- Quarterly cadence (no schedule — maintainer triggers).
- On demand whenever a session shows `EstimatedCostUSD: 0` for a model the maintainer recognizes as a real provider (likely a new alias upstream).
- Before cutting a release, if /ir:release's Step 1.5 wasn't run for some reason.

## Workflow

### 1. Fetch upstream

```bash
curl -fsSL https://raw.githubusercontent.com/getagentseal/codeburn/main/src/models.ts > /tmp/codeburn-models.ts
```

If the fetch fails, stop and report the error — don't proceed with a stale copy.

### 2. Extract upstream BUILTIN_ALIASES

```bash
awk '/^const BUILTIN_ALIASES/,/^}$/' /tmp/codeburn-models.ts
```

The block is a single TypeScript object literal: `'key': 'value',` per line, with grouping comments. Parse into an `alias → canonical` map. Ignore comment lines.

### 3. Load current Irrlicht map

Read `core/pkg/capacity/aliases.go` and extract entries from `var modelAliases`. Each line has the form:

```go
"key": "value",
```

Strip whitespace, comments, and the surrounding `map[string]string{…}` boilerplate.

### 4. Diff

Compute four sets:

- **Added** — keys present upstream but not in irrlicht. Default action: append to the Go map.
- **Changed** — keys present in both but with a different canonical target. Rare; flag for maintainer review (could indicate a model rename or a codeburn correction).
- **Removed** — keys in irrlicht but not upstream. Don't auto-delete; codeburn may have dropped something we still need. Flag with the entry contents.
- **Unchanged** — no action.

### 5. Validate canonical targets against LiteLLM

For each Added/Changed entry, check that the canonical value is a key in the running daemon's LiteLLM-derived table. The daemon caches it at `~/.local/share/irrlicht/model-capacity-cache.json`:

```bash
jq -r '.config.models | keys[]' ~/.local/share/irrlicht/model-capacity-cache.json | grep -F "<canonical>"
```

Surface any aliases whose canonical isn't in LiteLLM — they will still resolve to `ModelCapacity{}` and need either a follow-up upstream LiteLLM entry or a different canonical choice.

### 6. Propose changes

Report to the maintainer:

```
codeburn BUILTIN_ALIASES sync (commit <sha-or-date>)

Added (N):
  "<alias>" → "<canonical>"        [LiteLLM: ok | missing]
  ...

Changed (N):
  "<alias>": "<old-canonical>" → "<new-canonical>"   [LiteLLM: ok | missing]
  ...

Removed (N) — not auto-deleted:
  "<alias>" → "<canonical>"
  ...
```

If diff is empty: report "no changes" and exit.

### 7. Apply

Edit `core/pkg/capacity/aliases.go` — add new entries in the appropriate grouping section, update changed canonicals. Preserve the existing per-group comments. Don't delete Removed entries unless explicitly approved.

Run `go test ./core/pkg/capacity/... -race -count=1` — the table-driven test enumerates every entry; new aliases need a canonical seed already covered, otherwise the test catches it.

Then, depending on mode:

- **Standalone**: commit on a fresh branch (`chore(capacity): sync BUILTIN_ALIASES with codeburn`) and open a PR with the diff report as the body.
- **Release-inline**: leave the change staged on the active release branch (the `/ir:release` Step 7a commit will pick it up). Mention added entries under "Fixed" in the release notes — `/ir:release` Step 1.5 has the suggested phrasing.

## Constraints

- Exact-match only. Never propose prefix/fuzzy logic — see `core/pkg/capacity/aliases.go` header comment and the `TestModelAliases_UnknownReturnsUnchanged` test.
- Don't touch `NormalizeModelName` in `core/pkg/tailer/parser.go`. That's a separate (Anthropic-shorthand) normalizer with different semantics.
- Don't add runtime-fetch logic. The map is bundled at build time on purpose — pricing must be deterministic and offline-safe.
