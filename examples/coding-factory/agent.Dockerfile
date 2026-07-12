# syntax=docker/dockerfile:1
#
# Coding-factory agent image: a Linux irrlichd co-located with the OpenAI Codex
# CLI in one container. The daemon observes `codex` via /proc + pidfd (same PID
# namespace) and reads ~/.codex/sessions on the same filesystem; it forwards OUT
# to an auth-enabled relay (IRRLICHT_RELAY_URL + IRRLICHT_RELAY_TOKEN). A tmux
# auto-driver (drive.sh) feeds codex small tasks so the row cycles live without
# a human. Mirrors examples/roundtrip/ (which does the same for claude).
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
RUN go build -trimpath -ldflags "-X main.Version=${VERSION}" \
    -o /out/irrlichd ./cmd/irrlichd

# ---- runtime: Node (Codex CLI) + the daemon, run as a non-root user ----
FROM node:${NODE_VERSION}-bookworm-slim AS runtime
# git: the scratch repo codex edits. tini: PID-1 reaper/signals. procps: debug.
# tmux + jq: the auto-driver drives codex's TUI via tmux and watches the rollout
# for `task_complete` with jq. curl: REQUIRED — the daemon's auto-installed hook
# POSTs via `curl … || true`; without it the hook silently no-ops and tool-use
# permission prompts never surface as `waiting` (#488).
# OpenAI Codex CLI (the real agent this testbed observes).
#
# Non-root `agent` — both codex and irrlichd run as this user, sharing $HOME so
# the daemon can read codex's transcripts under ~/.codex/sessions.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates git tini procps curl tmux jq \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @openai/codex \
    && useradd --create-home --shell /bin/bash agent
COPY --from=build /out/irrlichd /usr/local/bin/irrlichd
COPY examples/coding-factory/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY examples/coding-factory/drive.sh /usr/local/bin/drive.sh
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/drive.sh

USER agent
ENV HOME=/home/agent
# Seed a throwaway git repo for codex to operate on. Baked into the image (not a
# volume) so every container starts fresh. Codex launches here, so the session
# shows up under the project name "work".
RUN git config --global user.email "agent@coding-factory" \
    && git config --global user.name "agent" \
    && git config --global init.defaultBranch main \
    && mkdir -p /home/agent/work \
    && cd /home/agent/work \
    && git init -q \
    && printf '# scratch\n\nA throwaway repo for the irrlicht coding-factory demo.\n' > README.md \
    && git add -A \
    && git commit -qm "seed scratch repo"
WORKDIR /home/agent/work

# Agent state is intentionally NOT a volume: each container mints a FRESH
# relay-identity.json on start, so the three agents show as three distinct
# daemons/rows (#539/item E — cloned images that share an identity collapse into
# one). The entrypoint also rm's it defensively.
RUN mkdir -p /home/agent/.local/share/irrlicht /home/agent/.codex

# Defaults; overridden by compose. Loopback daemon bind (127.0.0.1:7837) is fine.
ENV IRRLICHT_RELAY_URL=ws://relay:7839
ENV CODEX_MODEL=gpt-4o-mini
ENTRYPOINT ["tini", "--", "/usr/local/bin/entrypoint.sh"]
