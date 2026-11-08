#!/usr/bin/env bash
# A stand-in for gh(1), used by tools/lib/pr-merge-candidates_test.sh. It
# serves recorded payloads from a fixture world directory and answers only the
# two read forms tools/pr-merge-candidates.sh is allowed to use:
#
#   gh pr list ... --json <fields>          → <world>/pr-list.json
#   gh api [--paginate] <endpoint>          →
#     repos/{owner}/{repo}/collaborators?…  → <world>/collaborators.json
#     repos/{owner}/{repo}/issues/N/comments?…  → <world>/N/comments.json
#     repos/{owner}/{repo}/pulls/N/files?…      → <world>/N/files.json
#     repos/{owner}/{repo}/pulls/N/commits?…    → <world>/N/commits.json
#     repos/{owner}/{repo}/git/trees/SHA?…      → <world>/N/tree.json, where N
#                                                 is the PR whose headRefOid is SHA
#
# Anything else exits 64 — including any write form — so a script that grows a
# call this stub does not know fails loudly instead of reading an empty answer.
# A known endpoint whose fixture file is missing answers like a 404.
#
# With --paginate a JSON array is printed as pages of two elements back to
# back, the way real `gh api --paginate` prints one array per page, so the
# caller's page-joining is exercised on every run.
#
# Knobs, all environment variables:
#   PRM_STUB_WORLD=<dir>       the fixture world (required)
#   PRM_STUB_LOG=<file>        append every invocation's arguments, one line each
#   PRM_STUB_FAIL=1            exit 1 with an error on stderr and nothing on
#                              stdout, as gh does when it cannot connect
#   PRM_STUB_FAIL_ON=<text>    when the joined arguments contain <text>, fail the
#                              way `gh api` fails on an HTTP error: the JSON error
#                              body on STDOUT, a message on stderr, exit 1.
#                              Measured on gh 2.97.0, 2026-11-08: a tree request
#                              for an unknown SHA printed
#                              {"message":"Invalid object requested. …","status":"422"}
#                              on stdout and exited 1. That body is a valid JSON
#                              object, so only the exit status tells it apart
#                              from an answer.
#   PRM_STUB_GARBAGE_ON=<text> exit 0 but print non-JSON when the joined
#                              arguments contain <text>
set -uo pipefail

world=${PRM_STUB_WORLD:?gh-stub: PRM_STUB_WORLD is not set}
joined="$*"

if [ -n "${PRM_STUB_LOG:-}" ]; then
  printf '%s\n' "$joined" >>"$PRM_STUB_LOG"
fi

if [ "${PRM_STUB_FAIL:-}" = 1 ]; then
  echo "error connecting to api.github.com" >&2
  exit 1
fi
http_error() { # http_error <status> <message>
  printf '{"message":"%s","documentation_url":"https://docs.github.com/rest","status":"%s"}\n' "$2" "$1"
  echo "gh: $2 (HTTP $1)" >&2
  exit 1
}
if [ -n "${PRM_STUB_FAIL_ON:-}" ]; then
  case "$joined" in
    *"$PRM_STUB_FAIL_ON"*) http_error 422 "Invalid object requested" ;;
  esac
fi
if [ -n "${PRM_STUB_GARBAGE_ON:-}" ]; then
  case "$joined" in
    *"$PRM_STUB_GARBAGE_ON"*) echo "<html><body>502 Bad Gateway</body></html>"; exit 0 ;;
  esac
fi

serve() { # serve <file> <paginate:0|1>
  [ -f "$1" ] || http_error 404 "Not Found (gh-stub has no fixture $1)"
  if [ "$2" = 1 ] && jq -e 'type == "array"' "$1" >/dev/null 2>&1; then
    # Pages of two; an empty array is still one page, as real gh prints `[]`.
    jq -c 'if length == 0 then [] else [range(0; length; 2) as $i | .[$i:$i+2]][] end' "$1"
  else
    cat "$1"
  fi
  exit 0
}

case "${1:-} ${2:-}" in
  'pr list') serve "$world/pr-list.json" 0 ;;
esac

if [ "${1:-}" != api ]; then
  echo "gh-stub: unexpected command '$joined' — only 'pr list' and 'api' are served" >&2
  exit 64
fi
shift
paginate=0
endpoint=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --paginate) paginate=1 ;;
    -*) echo "gh-stub: unexpected api flag '$1' in '$joined'" >&2; exit 64 ;;
    *) [ -z "$endpoint" ] || { echo "gh-stub: two endpoints in '$joined'" >&2; exit 64; }
       endpoint=$1 ;;
  esac
  shift
done

path=${endpoint%%\?*}
case "$path" in
  'repos/{owner}/{repo}/collaborators') serve "$world/collaborators.json" "$paginate" ;;
  'repos/{owner}/{repo}/issues/'*/comments)
    n=${path#repos/\{owner\}/\{repo\}/issues/}; n=${n%/comments}
    serve "$world/$n/comments.json" "$paginate" ;;
  'repos/{owner}/{repo}/pulls/'*/files)
    n=${path#repos/\{owner\}/\{repo\}/pulls/}; n=${n%/files}
    serve "$world/$n/files.json" "$paginate" ;;
  'repos/{owner}/{repo}/pulls/'*/commits)
    n=${path#repos/\{owner\}/\{repo\}/pulls/}; n=${n%/commits}
    serve "$world/$n/commits.json" "$paginate" ;;
  'repos/{owner}/{repo}/git/trees/'*)
    sha=${path#repos/\{owner\}/\{repo\}/git/trees/}
    n=$(jq -r --arg s "$sha" '.[] | select(.headRefOid == $s) | .number' "$world/pr-list.json")
    [ -n "$n" ] || http_error 404 "Not Found (no PR in the stub world has head $sha)"
    serve "$world/$n/tree.json" "$paginate" ;;
esac

echo "gh-stub: unexpected endpoint '$endpoint'" >&2
exit 64
