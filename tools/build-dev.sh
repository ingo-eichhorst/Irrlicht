#!/usr/bin/env bash
# build-dev.sh — build the irrlichd dev binary with the right ldflags.
#
# Output: core/bin/irrlichd (the path the daemon is conventionally run
# from during local development — `cd core && ./bin/irrlichd --record`).
#
# Differs from tools/build-release.sh in three ways:
#   1. Single-platform native build (no GOOS×GOARCH fan-out).
#   2. Version string includes git sha and `.dirty` flag, so re-recordings
#      can tell which dev build produced each artifact (the daemon_version
#      field on archive + Latest manifests carries through to the viewer).
#   3. No code signing / app bundle / installer — just the binary.
#
# Usage:
#   tools/build-dev.sh          — builds irrlichd
#   tools/build-dev.sh focus    — also builds irrlicht-focus
#   tools/build-dev.sh all      — builds everything in core/cmd/

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VERSION="$("$SCRIPT_DIR/version.sh")"

cd "$ROOT_DIR/core"
mkdir -p bin

build_one() {
  local pkg="$1" out="$2"
  echo "  $out  ($VERSION)"
  go build -ldflags "-X main.Version=$VERSION" -o "bin/$out" "$pkg"
}

case "${1:-irrlichd}" in
  irrlichd)
    build_one "./cmd/irrlichd"       "irrlichd"
    ;;
  focus)
    build_one "./cmd/irrlichd"       "irrlichd"
    build_one "./cmd/irrlicht-focus" "irrlicht-focus"
    ;;
  all)
    for d in cmd/*/; do
      pkg="./$d"
      out="$(basename "$d")"
      build_one "$pkg" "$out"
    done
    ;;
  *)
    echo "usage: build-dev.sh [irrlichd|focus|all]" >&2
    exit 2
    ;;
esac

echo "done."
echo "Run isolated from the production daemon (separate state dir + port):"
echo "  IRRLICHT_HOME=\"\$PWD/.build/irrlicht-home\" IRRLICHT_BIND_ADDR=127.0.0.1:7838 \\"
echo "    core/bin/irrlichd --record"
