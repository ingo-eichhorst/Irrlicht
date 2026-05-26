#!/usr/bin/env bash
# replay-in-docker.sh — run irrlicht's Linux verification gate hermetically in
# Docker, so a macOS dev can reproduce the Linux CI run (replay goldens +
# replay-fixtures + the /proc+pidfd observer-conformance test) without a VM.
#
# Usage:
#   tools/replay-in-docker.sh                      # linux/amd64 + linux/arm64
#   PLATFORMS=linux/amd64 tools/replay-in-docker.sh
#
# arm64 on an Apple-Silicon Mac runs under Docker Desktop's QEMU emulation —
# slower, but it covers the actual deploy target (Ubuntu ARM, per #179).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
DOCKERFILE="tools/linux-replay.Dockerfile"

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }

cd "$ROOT"
IFS=',' read -ra plats <<< "$PLATFORMS"
for plat in "${plats[@]}"; do
    tag="irrlicht-linux-replay:${plat//\//-}"
    echo "=============================================================="
    echo "  building + running Linux gate for $plat"
    echo "=============================================================="
    docker buildx build --platform "$plat" --load -f "$DOCKERFILE" -t "$tag" .
    docker run --rm --platform "$plat" "$tag"
done

echo "all platforms passed: $PLATFORMS"
