# Hermetic Linux verification for irrlicht.
#
# Two complementary things run here — do NOT read a green run as "Linux
# observation works end to end":
#   1. Replay goldens + replay-fixtures — pure Go + virtual time, no syscalls.
#      Validates the parser → tailer → state-machine layers; deterministic and
#      OS-neutral (identical output on any OS). This is Stage 2.
#   2. The Linux observer-conformance test (processlifecycle, //go:build linux)
#      — spawns a real process and exercises /proc discovery + pidfd exit
#      watching. This is the Stage-1 sensor gate that replay deliberately does
#      not cover.
#
# Used both by CI and by tools/replay-in-docker.sh so a macOS dev can run the
# exact Linux gate locally (incl. arm64 via buildx/QEMU).
ARG GO_VERSION=1.25
FROM golang:${GO_VERSION}-bookworm

WORKDIR /src
COPY . .

# Compile gate: nothing below matters if the tree doesn't build on Linux.
RUN cd core && go build ./...

# Run the cross-platform gate. Kept as the image's default command (not a
# RUN) so `docker run` re-executes it against the built layers and a fresh
# process table for the conformance test.
CMD set -eux; \
    cd /src/core; \
    go test ./cmd/replay/... -count=1; \
    go test ./adapters/inbound/agents/processlifecycle/... -count=1; \
    cd /src; \
    tools/replay-fixtures.sh
