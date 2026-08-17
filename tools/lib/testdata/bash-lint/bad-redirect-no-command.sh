#!/usr/bin/env bash
# SC2188 — a redirection with no command. It works in bash (it truncates), but
# it is indistinguishable from a line whose command was accidentally deleted,
# and `: > "$log"` says the truncation was meant.
set -uo pipefail

log="$(mktemp)"
> "$log"
printf 'ok\n' >> "$log"
