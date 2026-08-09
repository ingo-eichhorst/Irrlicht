#!/bin/sh
# POSIX-clean, but shellcheck-NOISY: the unquoted expansions below trip
# SC2086 and friends, which are general style debt and deliberately OUT of
# this gate's scope.
#
# This fixture is what pins the severity filter. Every bad-* fixture exercises
# the MATCH direction (an SC3xxx is found and reported); without a file that
# makes shellcheck exit 1 carrying only non-POSIX-class codes, the filter
# itself is untested — deleting the code test and accepting every line would
# leave the whole suite green, while site/install.sh would start failing this
# gate on any SC2086.
#
# Must PASS.
x=$1
echo $x

d=$2
cd $d || exit 1
