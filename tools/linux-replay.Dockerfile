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
# known_failing gate); the slim golang image ships neither.
#
# Also runs as a non-root user rather than the image's root default
# (docker:S6471) — this container only ever runs our own test suite, but
# least privilege costs nothing here. Pre-create + chown /src here so
# replay-fixtures.sh can mkdir its own .build/ output dir there later;
# WORKDIR below just cds into it without touching ownership. One RUN
# (docker:S7031) for the whole setup layer.
RUN apt-get update \
    && apt-get install -y --no-install-recommends python3 jq \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 runner \
    && mkdir -p /src \
    && chown runner:runner /src
ENV HOME=/home/runner
WORKDIR /src

# Explicit, scoped copies rather than `COPY . .` (docker:S6470) — this is
# everything the compile gate + replay-fixtures.sh actually touch (core/ and
# tools/ resolve each other via the workspace file; replaydata/ holds the
# fixtures being replayed) and, just as importantly, everything it can NEVER
# pick up: repo-root files like a local .env never enter the build context.
COPY --chown=runner:runner go.work go.work.sum ./
COPY --chown=runner:runner core/ ./core/
COPY --chown=runner:runner tools/ ./tools/
COPY --chown=runner:runner replaydata/ ./replaydata/
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
    cd /src/tools/onboarding-factory; \
    go test ./cmd/replay/... -race -count=1; \
    cd /src/core; \
    go test ./adapters/inbound/agents/processlifecycle/... -race -count=1; \
    cd /src; \
    tools/replay-fixtures.sh
