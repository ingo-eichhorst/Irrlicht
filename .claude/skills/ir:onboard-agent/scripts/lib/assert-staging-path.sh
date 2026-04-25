# shellcheck shell=bash
# assert-staging-path.sh — refuse to operate outside .build/refresh/.
#
# Guards against ADAPTER/SCENARIO arguments containing path traversal ("..")
# that survived caller-side validation, and against accidentally pointing
# the staging dir at testdata/. Sourced (not exec'd) so it can fail-fast in
# the caller's process.
#
# Caller must set $STAGING and $REPO_ROOT.

if [[ "$STAGING" != "$REPO_ROOT/.build/refresh/"* ]] || [[ "$STAGING" == *"/testdata/"* ]]; then
  echo "refusing to stage outside .build/refresh/: $STAGING" >&2
  exit 1
fi
