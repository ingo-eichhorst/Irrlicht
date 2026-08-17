#!/usr/bin/env bash
# SC1087 (error severity) — `$var[` is read as an array expansion, so the
# intended `$var` followed by a literal `[` needs `${var}[`.
set -uo pipefail

agent=claudecode
grep -q "$agent[[:space:]]" /dev/null || true
