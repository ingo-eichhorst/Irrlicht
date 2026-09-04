#!/usr/bin/env bash
# web-release-assets-guard.sh — the release artifacts carry the WHOLE
# dashboard, not the part someone remembered to list.
#
# Usage:
#   tools/web-release-assets-guard.sh            # guard the repo's own tree
#   tools/web-release-assets-guard.sh --help
#
# Exit codes:
#   0  OK      — the staged tree is complete and carries nothing dev-only
#   1  FAIL    — a finding: a reachable asset is missing, a dev-only file
#                shipped, or build-release.sh stages by hand again
#   2  REFUSE  — could not judge (unreadable tree, no entry point, the import
#                scan found nothing). Never silence: "no finding" and "could
#                not look" must not print the same thing.
#
# ---------------------------------------------------------------------------
# What it guards, and why it is shaped this way (#1900)
#
# tools/build-release.sh staged the dashboard from a hand-written three-file
# `cp` list. That list was complete in #418 and stopped being complete in #712
# and #820, when irrlicht.js became an ES module importing ten siblings. Every
# release artifact after that shipped an irrlicht.js whose imports 404'd, so
# the dashboard rendered a blank page for every installed user on macOS and
# Linux — the only UI Linux has — until it was reported by hand.
#
# Three properties follow from that history, and each is load-bearing:
#
# 1. IT EXECUTES THE REAL STAGING RULE. The guard sources tools/lib/stage-web.sh
#    and stages into a temp dir, so what it inspects is what build-release.sh
#    will produce. A guard that re-implemented the rule would prove something
#    about itself and nothing about the script that ships. It also asserts
#    build-release.sh still routes every web copy through that function, so
#    the two cannot drift back apart.
#
# 2. IT WALKS THE TRANSITIVE CLOSURE, FROM index.html. A scan of irrlicht.js's
#    DIRECT imports finds 9 of the 10 modules and calls itself complete:
#    collapsedSet.js is reachable only through collapsedGroups.js and
#    collapsedSummaries.js. So the walk starts at index.html's <script
#    type=module> / <link rel=stylesheet>, follows `from '…'` edges, and keeps
#    a visited set — permissionsWizard.js and quotaChips.js both import back
#    from ./irrlicht.js, so the graph has cycles and a naive recursion would
#    not terminate.
#
# 3. IT FAILS LOUDLY WHEN IT CANNOT LOOK. The scan's own failure mode is
#    silence: irrlicht.js and sessionIdentity.js carry literal NUL bytes (an id
#    separator, `daemonId + '\0' + sessionId`), and a binary-detecting grep
#    skips such a file without a word — which would compute an EMPTY closure
#    and pass. Every read is `grep -a`, and a closure with no import edge in it
#    REFUSEs rather than reporting success. The same repo-wide hazard is
#    documented at tools/state-vocabulary-lint.sh:257.

set -uo pipefail

# _wrag_text_grep — grep over files that may contain literal NUL bytes. `-a`
# is the whole point (see property 3 above); `command` bypasses any grep shell
# function a sourcing caller may have.
_wrag_text_grep() { LC_ALL=C command grep -a "$@"; }

# _wrag_unquote — strip one layer of surrounding single, double or back quotes
# from the LAST quoted run on the line, which is where a module specifier and
# an HTML attribute value both sit.
_wrag_unquote() { sed -E "s/^.*['\"\`]([^'\"\`]*)['\"\`][^'\"\`]*\$/\1/"; }

# _wrag_decomment — drop single-line /* */ blocks and // line comments. A `//`
# only counts at the start of a line or after whitespace, so the `//` inside a
# `https://…` string literal survives.
_wrag_decomment() { LC_ALL=C sed -E -e 's,/\*.*\*/,,g' -e 's,(^|[[:space:]])//.*$,\1,'; }

# web_assets_html_refs <index.html> — one local asset reference per line, from
# <script … src=…> and <link … href=…>. External (`scheme://`), root-relative
# and in-page references are not assets this tree stages, and are dropped.
# <a href=…> is deliberately not read: index.html links out to irrlicht.io.
web_assets_html_refs() {
    local file="$1" refs
    refs=$(_wrag_text_grep -oE '<(script|link)[[:space:]][^>]*>' "$file" |
        _wrag_text_grep -oE '[[:space:]](src|href)[[:space:]]*=[[:space:]]*("[^"]*"|'\''[^'\'']*'\'')' |
        _wrag_unquote)
    printf '%s\n' "$refs" | while IFS= read -r r; do
        [[ -n "$r" ]] || continue
        case "$r" in
            *"://"* | /* | \#* | data:* | mailto:*) continue ;;
        esac
        printf '%s\n' "$r"
    done
}

# web_assets_js_edges <file> — one module specifier per line, for every
# `from '…'`, `import '…'`, `import('…')` and `new URL('…', import.meta.url)`
# in <file>. Single, double and back quotes are all read.
#
# The `from` anchor is the one that matters: 5 of this tree's 15 edges are
# multi-line import lists whose `from` clause sits on its own line, so a
# pattern anchored on `^import` would miss a third of the graph.
#
# Two stages, and the split is load-bearing in both directions. Stage 1 reads
# the FILE with `-a`, so a module carrying a literal NUL still yields its lines
# (property 3 in the header) — without it a NUL-containing module answers
# "Binary file … matches" and contributes nothing. Stage 2 strips comments from
# those few candidate lines before the specifier is extracted, so a
# commented-out import — an ordinary thing to leave behind during a refactor —
# is not read as a live edge and does not turn the release gate red for a file
# nobody actually loads.
web_assets_js_edges() {
    local file="$1"
    _wrag_text_grep -E "(^|[^[:alnum:]_\$.])(from|import|URL)[[:space:]]*\(?[[:space:]]*['\"\`]" "$file" |
        LC_ALL=C tr -d '\000' |
        _wrag_decomment |
        LC_ALL=C command grep -oE "(^|[^[:alnum:]_\$.])(from|import|new[[:space:]]+URL)[[:space:]]*\(?[[:space:]]*('[^']*'|\"[^\"]*\"|\`[^\`]*\`)" |
        _wrag_unquote
}

# web_assets_css_refuses_unfollowable <file> — this walker follows HTML and
# JavaScript references, not CSS ones. That is fine only while the dashboard's
# stylesheet has none, so rather than ignoring CSS silently — which is how a
# `@import './theme/dark.css'` would ship a 404 through a green gate — the one
# stylesheet is checked for references the walker could not follow. `data:` and
# external URLs are not references to anything this tree stages.
# Returns 0 when there is nothing unfollowable, 1 when there is.
web_assets_css_refuses_unfollowable() {
    local file="$1" hits
    hits=$(_wrag_text_grep -oE "(@import[[:space:]]*[^;]*|url\([^)]*\))" "$file" |
        LC_ALL=C command grep -vE "url\([[:space:]]*['\"]?(data:|https?:|#)" || :)
    [[ -z "$hits" ]] && return 0
    echo "FAIL: $file carries a CSS reference this walker does not follow, so its target is not in the closure and could ship as a 404:" >&2
    printf '%s\n' "$hits" | sed 's/^/       /' >&2
    return 1
}

# web_assets_closure <src-dir> — every file reachable from <src-dir>/index.html,
# one basename per line, sorted, index.html included.
#
# Two non-zero statuses, matching the header's exit table rather than blurring
# into it:
#   1  FAIL   — the walk read the tree and found a defect in it: a reference
#              that resolves to nothing, or a reference into a subdirectory /
#              outside the tree that the non-recursive staging rule could never
#              ship. The closure is not emitted, because it would be wrong.
#   2  REFUSE — the walk could not judge: no tree, no index.html, no entry
#              point, no module reached, or a JS graph with not one import edge
#              in it (which is what an unreadable tree looks like).
web_assets_closure() {
    local src="$1" seen="" queue="" cur spec base specs refs bad=0 edges=0 jsfiles=0

    if [[ -z "$src" || ! -d "$src" ]]; then
        echo "REFUSE: web asset closure — source tree not found: ${src:-<empty>}" >&2
        return 2
    fi
    if [[ ! -f "$src/index.html" ]]; then
        echo "REFUSE: web asset closure — $src/index.html is missing; there is no entry point to walk from" >&2
        return 2
    fi

    _wrag_enqueue() {
        local ref="$1" from="$2" b
        b=${ref#./}
        case "$b" in
            ../*)
                echo "FAIL: $from references '$ref', which lives outside the web tree — the staging rule is non-recursive and could never ship it" >&2
                return 1
                ;;
            */*)
                echo "FAIL: $from references '$ref' in a subdirectory — the staging rule is non-recursive and would silently drop it" >&2
                return 1
                ;;
        esac
        if [[ ! -f "$src/$b" ]]; then
            echo "FAIL: $from references '$ref', which does not exist in $src" >&2
            return 1
        fi
        case $'\n'"$seen" in *$'\n'"$b"$'\n'*) return 0 ;; esac
        seen="$seen$b"$'\n'
        queue="$queue$b"$'\n'
        return 0
    }

    seen=$'\n'"index.html"$'\n'
    refs=$(web_assets_html_refs "$src/index.html")
    if [[ -z "$refs" ]]; then
        echo "REFUSE: web asset closure — $src/index.html yielded no <script src> or <link href> reference; the scan could not read its entry points" >&2
        return 2
    fi
    while IFS= read -r spec; do
        [[ -n "$spec" ]] || continue
        _wrag_enqueue "$spec" "index.html" || bad=1
    done <<<"$refs"

    # Breadth-first over the queue, with `seen` as the visited set: the graph
    # is cyclic (permissionsWizard.js and quotaChips.js import back from
    # irrlicht.js), so re-entry has to be cut here, not by recursion depth.
    while [[ -n "$queue" ]]; do
        cur=${queue%%$'\n'*}
        queue=${queue#*$'\n'}
        [[ -n "$cur" ]] || continue
        case "$cur" in *.js) ;; *) continue ;; esac
        jsfiles=$((jsfiles + 1))
        specs=$(web_assets_js_edges "$src/$cur")
        while IFS= read -r spec; do
            [[ -n "$spec" ]] || continue
            # Non-relative specifiers are bare package / node: builtins. They
            # do not come from this tree, so they are not this rule's business.
            case "$spec" in ./* | ../*) ;; *) continue ;; esac
            edges=$((edges + 1))
            _wrag_enqueue "$spec" "$cur" || bad=1
        done <<<"$specs"
    done

    if [[ "$jsfiles" -eq 0 ]]; then
        echo "REFUSE: web asset closure — index.html reached no JavaScript module at all" >&2
        return 2
    fi
    if [[ "$edges" -eq 0 ]]; then
        echo "REFUSE: web asset closure — scanned $jsfiles module(s) and found not one import edge. The scan cannot read this tree (a NUL-containing module read as binary would look exactly like this), so an 'everything is staged' verdict would be vacuous." >&2
        return 2
    fi
    # A dangling or unstageable reference is a FINDING about the tree, not an
    # inability to look at it — and _wrag_enqueue already said `FAIL:`.
    [[ "$bad" -eq 0 ]] || return 1

    base=${seen#$'\n'}
    printf '%s' "$base" | LC_ALL=C sort -u
}

# web_assets_guard <repo-root> — the whole check. 0 ok / 1 finding / 2 refuse.
web_assets_guard() {
    local root="$1" src="$1/platforms/web" brs="$1/tools/build-release.sh"
    local closure staged tmp rc=0 f

    if [[ ! -f "$root/tools/lib/stage-web.sh" ]]; then
        echo "REFUSE: tools/lib/stage-web.sh is missing — the staging rule this guard executes is gone" >&2
        return 2
    fi
    # shellcheck source=lib/stage-web.sh
    . "$root/tools/lib/stage-web.sh"

    closure=$(web_assets_closure "$src") || return $?

    # The one stylesheet is checked for references this walker cannot follow,
    # rather than CSS being skipped in silence.
    web_assets_css_refuses_unfollowable "$src/irrlicht.css" || rc=1

    # An explicit template, not `-t <prefix>`: GNU mktemp requires at least
    # three trailing X's and errors out on a bare prefix, so the `-t` spelling
    # would take the REFUSE branch below on every Linux host.
    tmp=$(mktemp -d "${TMPDIR:-/tmp}/web-release-assets-guard.XXXXXX") || {
        echo "REFUSE: could not create a staging directory" >&2
        return 2
    }
    # shellcheck disable=SC2064  # $tmp is expanded now, on purpose.
    trap "rm -rf '$tmp'" RETURN

    if ! stage_web "$src" "$tmp/web"; then
        echo "REFUSE: stage_web refused to stage $src — the release staging rule cannot run" >&2
        return 2
    fi
    staged=$(cd "$tmp/web" && LC_ALL=C ls -A | LC_ALL=C sort)
    if [[ -z "$staged" ]]; then
        echo "REFUSE: stage_web reported success but staged nothing" >&2
        return 2
    fi

    # (a) every reachable asset ships.
    while IFS= read -r f; do
        [[ -n "$f" ]] || continue
        case $'\n'"$staged"$'\n' in
            *$'\n'"$f"$'\n'*) ;;
            *)
                echo "FAIL: '$f' is reachable from index.html but tools/build-release.sh would not stage it — it will 404 in every release artifact" >&2
                rc=1
                ;;
        esac
    done <<<"$closure"

    # (b) nothing dev-only ships. Without this half a lazy `cp -R` of the whole
    # web tree — node_modules and all — would satisfy (a) and read as a fix.
    while IFS= read -r f; do
        [[ -n "$f" ]] || continue
        case "$f" in
            *.test.js | vitest.* | package.json | package-lock.json | node_modules | snapshots | .* )
                echo "FAIL: '$f' is dev-only tooling and must not ship in a release artifact" >&2
                rc=1
                ;;
        esac
        if [[ -d "$tmp/web/$f" ]]; then
            echo "FAIL: '$f' is a directory — the release web tree is flat, and a recursive copy is how node_modules ships by accident" >&2
            rc=1
        fi
    done <<<"$staged"

    # (c) build-release.sh still routes EVERY web copy through the rule. (a)
    # and (b) grade tools/lib/stage-web.sh; this is what keeps the shipping
    # script attached to it, so a fourth artifact cannot reintroduce a
    # hand-written list beside it.
    if [[ ! -f "$brs" ]]; then
        echo "REFUSE: tools/build-release.sh is missing" >&2
        return 2
    fi
    if ! _wrag_text_grep -q 'lib/stage-web\.sh' "$brs"; then
        echo "FAIL: tools/build-release.sh does not source tools/lib/stage-web.sh" >&2
        rc=1
    fi
    if ! _wrag_text_grep -qE '(^|[^[:alnum:]_])stage_web[[:space:]]' "$brs"; then
        echo "FAIL: tools/build-release.sh never calls stage_web — nothing stages the dashboard by rule" >&2
        rc=1
    fi
    local handrolled
    handrolled=$(_wrag_text_grep -nE 'platforms/web' "$brs" |
        _wrag_text_grep -vE '^[0-9]+:[[:space:]]*#' |
        _wrag_text_grep -vE '(^|[^[:alnum:]_])stage_web[[:space:]]' || :)
    if [[ -n "$handrolled" ]]; then
        echo "FAIL: tools/build-release.sh touches platforms/web outside stage_web — that is the hand-written list this guard exists to prevent:" >&2
        printf '%s\n' "$handrolled" | sed 's/^/       tools\/build-release.sh:/' >&2
        rc=1
    fi

    # (d) ...and neither does the procedure a release is actually DRIVEN from.
    # .claude/skills/ir:release/SKILL.md carries manual per-artifact build
    # blocks as a fallback, and every one of them used to `cp` the web tree by
    # hand — two of them copying index.html alone, which is #1900 with one file
    # instead of three. Fixing only build-release.sh would leave the defect
    # sitting in the document the maintainer reads.
    local skill="$root/.claude/skills/ir:release/SKILL.md"
    if [[ ! -f "$skill" ]]; then
        echo "REFUSE: .claude/skills/ir:release/SKILL.md is missing — cannot check the release procedure for a hand-written copy list" >&2
        return 2
    fi
    local skillcp
    skillcp=$(_wrag_text_grep -nE '(^|[^[:alnum:]_])(cp|rsync|ditto)[[:space:]].*platforms/web' "$skill" || :)
    if [[ -n "$skillcp" ]]; then
        echo "FAIL: .claude/skills/ir:release/SKILL.md copies platforms/web by hand — the release procedure must call stage_web, like tools/build-release.sh does:" >&2
        printf '%s\n' "$skillcp" | sed 's/^/       .claude\/skills\/ir:release\/SKILL.md:/' >&2
        rc=1
    fi

    if [[ "$rc" -eq 0 ]]; then
        echo "OK: web-release-assets-guard — $(printf '%s\n' "$closure" | _wrag_text_grep -c .) reachable asset(s) from index.html, all staged by tools/lib/stage-web.sh, no dev-only file in the release tree, no hand-written copy list in build-release.sh or the release skill"
    fi
    return "$rc"
}

if [[ "${BASH_SOURCE[0]:-}" == "${0:-}" ]]; then
    case "${1:-}" in
        -h | --help)
            awk 'NR>1 && /^#/ {print; next} NR>1 {exit}' "$0"
            exit 0
            ;;
        "") ;;
        *)
            echo "REFUSE: unknown argument '$1' (try --help)" >&2
            exit 2
            ;;
    esac
    WRAG_ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || {
        echo "REFUSE: not inside a git repository — cannot locate the web tree" >&2
        exit 2
    }
    web_assets_guard "$WRAG_ROOT"
    exit $?
fi
