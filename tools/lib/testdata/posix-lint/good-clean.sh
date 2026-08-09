#!/bin/sh
# Clean POSIX sh. Must PASS — the vacuity guard for the bad-* fixtures: a
# linter that failed everything would satisfy all of them and still be useless.
set -eu

greet() {
    printf '%s\n' "hello $1"
}

s="a"
s="${s}b"
v="ABC"
if [ "$s" = "ab" ]; then
    greet "$v"
fi

for f in one two; do
    printf '%s\n' "$f"
done
