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

# replay-fixtures.sh shells out to python3 (report rendering) and jq (the
# known_failing gate); the slim golang image ships neither. Install them in a
# cached layer before COPY so source edits don't re-run apt.
RUN apt-get update \
    && apt-get install -y --no-install-recommends python3 jq \
    && rm -rf /var/lib/apt/lists/*

# Run the build/test as a non-root user rather than the image's root default
# (docker:S6471) — this container only ever runs our own test suite, but
# least privilege costs nothing here.
RUN useradd --create-home --uid 10001 runner
ENV HOME=/home/runner
WORKDIR /src
COPY --chown=runner:runner . .
USER runner

# Compile gate: nothing below matters if the tree doesn't build on Linux.
RUN cd core && go build ./...

# Run the cross-platform gate. Kept as the image's default command (not a
# RUN) so `docker run` re-executes it against the built layers and a fresh
# process table for the conformance test.
# -race matches the linux.yml CI job — the conformance test exists to catch
# pidfd/poll concurrency bugs, so the local harness must exercise the detector
# too, or a race could pass here and fail CI.
CMD set -eux; \
    cd /src/core; \
    go test ./cmd/replay/... -race -count=1; \
    go test ./adapters/inbound/agents/processlifecycle/... -race -count=1; \
    cd /src; \
    tools/replay-fixtures.sh
