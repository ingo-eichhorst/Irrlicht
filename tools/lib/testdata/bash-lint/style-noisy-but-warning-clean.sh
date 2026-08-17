#!/usr/bin/env bash
# The severity floor, pinned in the ONE direction the bad-*.sh files cannot
# reach. They all trip a warning or an error, so they exercise the "report it"
# arm; this file makes shellcheck exit 1 carrying only findings BELOW the
# floor — SC2086 (unquoted expansion, info) and SC2012 (`ls | wc`, style).
#
# Without it, lowering the floor to `style` — or dropping --severity
# altogether — leaves the whole suite green while 199 findings land on files
# nobody has linted before, 163 of which CI's shellcheck 0.9.0 and a local
# 0.11.0 disagree about (see tools/bash-lint.sh's header for the measurement).
set -uo pipefail

dir=$1
echo $dir
count=$(ls "$dir" | wc -l)
echo "$count"
