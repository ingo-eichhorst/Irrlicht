# syntax=docker/dockerfile:1
#
# Standalone irrlichtrelay — the cross-host hub that daemons forward to and
# clients (the macOS app or a browser) read from. Built from source in this
# repo; no published release needed.
#
# Build context is the REPO ROOT (the compose files set `context: ../..`), so
# the builder can compile from ./core and copy ./platforms/web.
ARG GO_VERSION=1.25
ARG VERSION=docker

# ---- builder: compile a static Linux irrlichtrelay ----
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
    -o /out/irrlichtrelay ./cmd/irrlichtrelay

# Stage the dashboard by the SAME rule the release uses, never by a file list.
# These COPYs sit after the go build so a web edit does not invalidate it.
# tools/lib/stage-web.sh is the one definition of the runtime asset set; a
# hand-written list here is exactly what shipped a blank dashboard to every
# install for three releases (#1900), and it went stale the same way this
# image's own "only the three runtime files" comment had.
COPY tools/lib/stage-web.sh /stage-web.sh
COPY platforms/web/ /src/webtree/
RUN bash -c '. /stage-web.sh && stage_web /src/webtree /out/web'

# ---- runtime: the binary + a baked-in dashboard snapshot ----
FROM debian:bookworm-slim AS runtime
# Run as a non-root user rather than the image's root default (docker:S6471)
# — 7839 is unprivileged so this needs no capability grants.
RUN useradd --no-create-home --uid 10001 relay
COPY --from=build /out/irrlichtrelay /usr/local/bin/irrlichtrelay
# Bake the live dashboard so http://<host>:7839 serves it. NOTE: this is a
# snapshot — unlike the daemon's live disk-serve, web edits need an image
# rebuild. The tree comes from the build stage, already reduced to the runtime
# set by stage_web (no tests, no vitest config, no node_modules, no snapshots).
COPY --from=build --chown=relay:relay /out/web/ /web/
ENV IRRLICHT_UI_DIR=/web
USER relay
EXPOSE 7839
# Bind 0.0.0.0 (NOT the loopback default) so the published port is reachable
# from outside the container; the compose file publishes it on the host
# loopback. Auth/TLS are OFF here — fine for a trusted host. Before exposing on
# a LAN/the internet, add `--auth tokens-file` and front it with TLS (or
# `--tls-cert`/`--tls-key`); see DEPLOY.md.
CMD ["irrlichtrelay", "serve", "--addr", "0.0.0.0:7839"]
