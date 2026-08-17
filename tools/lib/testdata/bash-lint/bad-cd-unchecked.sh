#!/usr/bin/env bash
# SC2164 — `cd` with no `|| exit`. When the cd fails the script keeps going in
# the WRONG directory, and everything after it operates on the caller's tree.
set -uo pipefail

cd /nonexistent-directory-for-the-fixture
rm -f ./generated-output
