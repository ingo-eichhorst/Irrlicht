#!/usr/bin/env bash
# SC2115 — `rm -rf "$var/lib"` becomes `rm -rf /lib` when $var is empty.
#
# This is the shape #1684 was filed about, and the reachability is the point:
# `set -u` catches an UNSET variable but not an EMPTY one, and `$(mktemp -d)`
# yields the empty string when mktemp fails. The fix is one character —
# "${scratch:?}/lib" — and the same shape removed ~1,895 files from a real
# ~/Library/Preferences during the work behind #1661.
set -uo pipefail

scratch="$(mktemp -d)"
rm -rf "$scratch/lib"
