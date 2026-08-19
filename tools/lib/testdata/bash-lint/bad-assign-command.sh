#!/usr/bin/env bash
# SC2209 — `var=command` assigns the STRING "command" and runs nothing, where
# `var=$(command)` was meant. An assertion downstream then compares against a
# literal and passes for the wrong reason.
set -uo pipefail

kind=file
printf '%s\n' "$kind"
