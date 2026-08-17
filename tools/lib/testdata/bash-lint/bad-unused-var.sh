#!/usr/bin/env bash
# SC2034 — assigned and never used. Often a genuine cross-file seam (a knob a
# sourced library reads, an output variable a caller reads), which is why the
# sanctioned answer is a per-site `# shellcheck disable=SC2034  # <reason>`
# rather than deleting the line — but it is equally often a variable renamed
# in one place and not the other, which is a live defect.
set -uo pipefail

WAS_RENAMED_HERE=1
printf 'but read under the old name: %s\n' "${WAS_RENAMED_THERE:-}"
