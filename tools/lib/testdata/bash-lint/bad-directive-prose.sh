#!/usr/bin/env bash
# SC1072/SC1073 — THE MOST IMPORTANT FIXTURE HERE, because its failure mode is
# silence. A comment line whose first word is the linter's own name is parsed
# as a DIRECTIVE, and one it cannot parse makes it ABANDON the file: every
# other finding below simply disappears.
#
# The SC2115 on the last line is what proves it. Reword line 12 so it is not a
# directive and shellcheck reports the rm; leave it and the rm is invisible.
# This is not hypothetical — replaydata/_lib/drive/contracts.sh has carried it
# unnoticed (measured), and tools/bash-lint.sh's own header tripped it on the
# gate's first run.
# shellcheck this line is prose, not a directive, and that is the defect
set -uo pipefail

scratch=""
rm -rf "$scratch/lib"
