#!/bin/sh
# Default command for the tools/linux-replay.Dockerfile image, extracted
# into a script and invoked in exec form (docker:S7019) rather than as an
# inline shell-form CMD. Still the image's CMD (not a RUN) so `docker run`
# re-executes it against the built layers with a fresh process table for
# the conformance test.
#
# -race matches the linux.yml CI job — the conformance test exists to catch
# pidfd/poll concurrency bugs, so this harness must exercise the detector
# too, or a race could pass here and fail CI.
set -eux
cd /src/tools/onboarding-factory
go test ./cmd/replay/... -race -count=1
cd /src/core
go test ./adapters/inbound/agents/processlifecycle/... -race -count=1
cd /src
tools/replay-fixtures.sh
