#!/usr/bin/env bash
# SC1125 — a directive whose reason is appended without a second `#`.
#
# Measured on both 0.9.0 and 0.11.0: the `disable=` IS still honoured (the
# SC2034 below does not fire), so this is not a broken suppression — it is an
# ERROR-severity complaint about the spelling, which is exactly what makes it
# worth catching, because the correct form is one character away and eight
# directives in replaydata/ are written this way.
set -uo pipefail

# shellcheck disable=SC2034 — set for a sourced consumer to read
SEAM_FOR_A_CONSUMER=1
