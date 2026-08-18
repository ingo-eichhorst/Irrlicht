#!/usr/bin/env bash
# seed-domains.sh — the ONE generator of `defaults export` bodies for
# defaults-stub.sh to answer with.
#
#   seed-domains.sh <dir>                    the baseline: the control domain
#                                            and both watched domains, present
#                                            and settled.
#   seed-domains.sh --one <path> [k=v]...    one domain body with those keys.
#   seed-domains.sh --empty <path>           what a domain that does not exist
#                                            reads as: status 0, `<dict/>`
#                                            (measured against the real binary).
#
# One generator rather than two, because every mutation swift-suite_test.sh
# drives is a delta FROM the baseline: a second, hand-kept copy of the body
# shape would make an unrelated formatting difference read as "every key
# changed", i.e. it would report the fixture rather than the defect.
#
# Two callers need it. shell-lib-errexit_test.sh's recipes assert a status of 0,
# and a row riding on the developer's live io.irrlicht.app would be a coin flip.
# swift-suite_test.sh drives the deltas.
#
# Every body carries a NESTED dictionary on purpose: the flattener's declared
# property is that only TOP-LEVEL keys are reported, and a corpus with nothing
# nested in it cannot tell a correct flattener from one that reports every key
# at every depth.

set -uo pipefail

_head() {
  printf '<?xml version="1.0" encoding="UTF-8"?>\n'
  printf '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n'
  printf '<plist version="1.0">\n'
}

seed_one() {
  local path="$1"; shift
  local kv
  {
    _head
    printf '<dict>\n'
    for kv in "$@"; do
      printf '\t<key>%s</key>\n\t<string>%s</string>\n' "${kv%%=*}" "${kv#*=}"
    done
    printf '\t<key>nested</key>\n\t<dict>\n\t\t<key>notTopLevel</key>\n\t\t<string>x</string>\n\t</dict>\n'
    printf '</dict>\n</plist>\n'
  } > "$path"
}

seed_empty() {
  { _head; printf '<dict/>\n</plist>\n'; } > "$1"
}

case "${1:-}" in
  --one)
    [[ -n "${2:-}" ]] || { echo "seed-domains.sh --one: needs <path>" >&2; exit 2; }
    path="$2"; shift 2
    seed_one "$path" "$@"
    ;;
  --empty)
    [[ -n "${2:-}" ]] || { echo "seed-domains.sh --empty: needs <path>" >&2; exit 2; }
    seed_empty "$2"
    ;;
  "")
    echo "seed-domains.sh: needs <dir>, --one <path> or --empty <path>" >&2
    exit 2
    ;;
  *)
    dir="$1"
    mkdir -p "$dir" || exit 2
    seed_one "$dir/${SWIFT_SUITE_WITNESS_CONTROL_DOMAIN:-NSGlobalDomain}" AppleLanguages=en
    seed_one "$dir/com.apple.dt.xctest.tool" soundOnReady=funk soundOnContextPressure=sosumi
    seed_one "$dir/io.irrlicht.app" projectGroupOrder=irrlicht
    ;;
esac
