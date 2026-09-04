#!/usr/bin/env bash
# stage-web.sh — the single definition of WHICH files of the web dashboard
# tree belong in a release artifact.
#
# This file is sourced, not executed: it defines functions and returns.
#
#   . "$SCRIPT_DIR/lib/stage-web.sh"
#   stage_web platforms/web "$TARBALL_STAGING/web"
#
# ---------------------------------------------------------------------------
# Why this is a library and not three `cp` lines (#1900)
#
# tools/build-release.sh staged the dashboard from a hand-written three-file
# list — index.html, irrlicht.css, irrlicht.js — written in #418, when those
# three WERE the whole dashboard. Since #712 and #820, irrlicht.js is an ES
# module that imports ten siblings, and none of them were on the list. Every
# import 404'd in every release artifact (the macOS .app, the darwin daemon
# tarball, both Linux daemon tarballs), the module graph never evaluated, and
# the shipped dashboard rendered a blank page for every installed user on both
# platforms until someone reported it.
#
# A hand-written list cannot notice a new module. A rule can. And a rule that
# lives in ONE place can be EXECUTED by a guard
# (tools/web-release-assets-guard.sh) rather than re-described by it — a guard
# that reimplements the rule proves nothing about the script that ships.
#
# It lives here, in tools/lib/, rather than inside build-release.sh, because
# build-release.sh runs a full signed universal build at top level under
# `set -e`: it cannot be sourced, so a guard could only re-parse its text.
# tools/lib/ is where this repo keeps its sourced shell libraries, and
# tools/lib/shell-lib-errexit_test.sh grades every function in it by
# discovering it.
#
# ---------------------------------------------------------------------------
# THE RULE
#
# Every *.html, *.css and *.js directly inside the web tree, minus the
# dev-only files (`*.test.js`, `vitest.config.js`, `vitest.setup.js`).
#
# The glob does not recurse, so `node_modules/` and `snapshots/` cannot be
# swept in no matter how large they get, and `package.json` /
# `package-lock.json` are not matched at all. Naming the runtime set by
# extension and subtracting the dev files — rather than listing the runtime
# files — is the half that makes a newly extracted module ship by default.

# stage_web_is_dev_only <basename> — 0 when the file is dev-only tooling that
# must never reach a release artifact, 1 when it is a runtime asset.
stage_web_is_dev_only() {
    case "$1" in
        *.test.js|vitest.config.js|vitest.setup.js) return 0 ;;
        *) return 1 ;;
    esac
}

# stage_web_list <src> — one runtime-asset basename per line, sorted.
#
# Refuses loudly rather than emitting an empty list: "the tree has no runtime
# assets" and "I could not look at the tree" must not print the same thing.
#   2  <src> is empty or is not a directory
#   3  the rule matched nothing at all
#   4  the matched set has no index.html, so this is not a dashboard tree
stage_web_list() {
    local src="$1" f base out=""
    if [[ -z "$src" || ! -d "$src" ]]; then
        echo "stage_web: source tree not found: ${src:-<empty>}" >&2
        return 2
    fi
    for f in "$src"/*.html "$src"/*.css "$src"/*.js; do
        [[ -f "$f" ]] || continue
        base=${f##*/}
        stage_web_is_dev_only "$base" && continue
        out="$out$base"$'\n'
    done
    if [[ -z "$out" ]]; then
        echo "stage_web: no runtime asset matched in $src — the staging rule found nothing to ship" >&2
        return 3
    fi
    case $'\n'"$out" in
        *$'\n'index.html$'\n'*) : ;;
        *)
            echo "stage_web: $src has no index.html — this is not a dashboard tree" >&2
            return 4
            ;;
    esac
    printf '%s' "$out" | LC_ALL=C sort
}

# stage_web <src> <dest> — copy the runtime asset set from <src> into <dest>,
# creating <dest>. Returns stage_web_list's status when the rule refuses, plus:
#   2  no destination given
#   5  <dest> could not be created
#   6  a matched file could not be copied
stage_web() {
    local src="$1" dest="$2" list base
    if [[ -z "$dest" ]]; then
        echo "stage_web: no destination given" >&2
        return 2
    fi
    list=$(stage_web_list "$src") || return $?
    if ! mkdir -p "$dest"; then
        echo "stage_web: could not create $dest" >&2
        return 5
    fi
    while IFS= read -r base; do
        [[ -n "$base" ]] || continue
        if ! cp "$src/$base" "$dest/$base"; then
            echo "stage_web: could not copy $base into $dest" >&2
            return 6
        fi
    done <<<"$list"
    return 0
}
