#!/usr/bin/env bash
# adapter-slug.sh — the shell rig's one copy of the column → daemon-adapter map.
#
# A column is a replaydata/agents/<slug> directory, and $ADAPTER in the rig
# scripts is that slug. The daemon names adapters by agent.Identity.Name. The
# two are equal for every column except the ones below, and every place the rig
# hands a slug to the daemon — or compares a slug with a name the daemon wrote —
# goes through adapter_slug_to_daemon_name:
#
#   claudecode       → claude-code  the pre-#319 registry spelling survives
#   deepseek-harness → dsh          the column is named after the product
#   opencode-v2      → opencode     OpenCode 2.x, recorded as its own column and
#                                   replayed through the opencode adapter (#2109)
#
# The Go original is shard.DaemonAdapterForSlug
# (tools/onboarding-factory/internal/shard/shard.go);
# TestShellSlugMapMatchesTheGoMap runs this function for every onboarded column
# and every mapped slug and fails on any disagreement.
#
# Why it matters: the daemon's lookups are exact-string. IRRLICHT_RECORD_ADAPTERS
# naming a slug scopes the recorded adapter's OWN hooks grant out
# (permission_service.go scopedOutByRecordAdapters), so no hook is installed;
# a `select(.agent == $slug)` over the daemon's unapplied_grants matches nothing
# and passes. Both failures produce a recording that looks healthy and has no
# hook signal in it.
#
# Usage:
#   source "$SCRIPT_DIR/lib/adapter-slug.sh"
#   name="$(adapter_slug_to_daemon_name "$ADAPTER")"

# adapter_slug_to_daemon_name <slug> prints the daemon's agent.Identity.Name
# for a column slug; a slug outside the map is printed unchanged.
adapter_slug_to_daemon_name() {
  case "$1" in
    claudecode) printf '%s' "claude-code" ;;
    deepseek-harness) printf '%s' "dsh" ;;
    opencode-v2) printf '%s' "opencode" ;;
    *) printf '%s' "$1" ;;
  esac
}
