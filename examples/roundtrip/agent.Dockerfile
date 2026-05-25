# syntax=docker/dockerfile:1
#
# Round-trip agent image: a Linux irrlichd co-located with Claude Code in one
# container. The daemon observes `claude` via /proc + pidfd (same PID
# namespace) and reads ~/.claude/projects on the same filesystem — mirroring a
# real Linux dev box. It forwards OUT to the relay (IRRLICHT_RELAY_URL); the
# user drives `claude` interactively via `docker compose exec`.
#
# Build context is the REPO ROOT (compose sets `context: ../..`).
ARG GO_VERSION=1.25
ARG NODE_VERSION=22
ARG VERSION=docker

# ---- builder: compile a static Linux irrlichd ----
FROM golang:${GO_VERSION}-bookworm AS build
ARG VERSION
ENV CGO_ENABLED=0
WORKDIR /src/core
COPY core/ ./
# Single-step build (go fetches + verifies against go.sum) — mirrors the CI
# linux-replay Dockerfile. A separate `go mod download` is avoided: it
# pre-populates the module cache in a way that makes `go build` demand go.sum
# go.mod hashes a clean pruned-graph build never needs.
RUN go build -trimpath -ldflags "-X main.Version=${VERSION}" \
    -o /out/irrlichd ./cmd/irrlichd

# ---- runtime: Node (Claude Code) + the daemon, run as a non-root user ----
FROM node:${NODE_VERSION}-bookworm-slim AS runtime
# git: the scratch repo + Claude's own tooling. tini: PID-1 reaper/signals.
# procps: handy for debugging inside the container. curl: REQUIRED — the
# daemon's auto-installed Claude Code hooks POST to the daemon via
# `curl … || true`; without curl the hook silently no-ops (the `|| true`
# swallows "command not found"), so PermissionRequest never reaches the
# daemon and tool-use permission prompts never surface as `waiting` (#488).
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git tini procps curl \
    && rm -rf /var/lib/apt/lists/*
# Claude Code CLI (the real agent this testbed observes).
RUN npm install -g @anthropic-ai/claude-code

# Non-root `agent` — avoids claude's run-as-root friction; both claude and
# irrlichd run as this user.
# (UID auto-assigned — the node base already uses 1000 for its `node` user.)
RUN useradd --create-home --shell /bin/bash agent
COPY --from=build /out/irrlichd /usr/local/bin/irrlichd
COPY examples/roundtrip/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

USER agent
ENV HOME=/home/agent
# Seed a throwaway git repo for `claude` to operate on. Baked into the image
# (not a volume) so it's fresh on every `up`. Claude launches here, so the
# session shows up on the Mac under the project name "work".
RUN git config --global user.email "agent@linux-dev" \
    && git config --global user.name "agent" \
    && git config --global init.defaultBranch main \
    && mkdir -p /home/agent/work \
    && cd /home/agent/work \
    && git init -q \
    && printf '# scratch\n\nA throwaway repo for the irrlicht round-trip demo.\n' > README.md \
    && git add -A \
    && git commit -qm "seed scratch repo"
WORKDIR /home/agent/work

# Pre-create the volume mount points owned by `agent`. Compose mounts named
# volumes here; if the paths don't already exist in the image, Docker creates
# them root-owned and the non-root daemon can't write its state (nor can claude
# log in). Creating them as `agent` first makes the fresh volumes agent-owned.
RUN mkdir -p /home/agent/.claude /home/agent/.local/share/irrlicht

# Default relay target; overridden by compose. Loopback daemon bind
# (127.0.0.1:7837) is fine — nothing connects to it directly.
ENV IRRLICHT_RELAY_URL=ws://relay:7839
ENTRYPOINT ["tini", "--", "/usr/local/bin/entrypoint.sh"]
