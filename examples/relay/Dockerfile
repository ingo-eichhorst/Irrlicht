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
# Pinned to the BUILD host's platform, with GOARCH taken from the target: the
# relay is pure Go with no cgo, so `docker buildx build --platform
# linux/amd64,linux/arm64` cross-compiles twice on the native toolchain instead
# of emulating a whole Go toolchain under QEMU for the foreign arch. Not a
# micro-optimisation here — the deployment target's free tier is Ampere A1
# (aarch64) with an amd64 fallback, so both arches get built routinely
# (docs/mobile-notifications-arc42.md §7).
#
# BUILDPLATFORM and TARGETARCH are supplied by BuildKit and by nothing else,
# so both carry a fallback for the classic builder, which this repo's own
# documented entry point (`docker compose … --build`) still lands on when the
# buildx plugin is absent. Measured on Docker 28.3.3 without buildx: an
# undefaulted `--platform=$BUILDPLATFORM` aborts the build with "failed to
# parse platform" before any step runs, while a defaulted one is validated and
# then IGNORED — asking for linux/amd64 on an arm64 host still built aarch64.
# The classic builder is single-platform by nature, so ignoring the pin is the
# correct behaviour there and the literal below is never the thing that
# decides an architecture.
FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:${GO_VERSION}-bookworm AS build
ARG VERSION
ARG TARGETARCH
ENV CGO_ENABLED=0
WORKDIR /src/core
COPY core/ ./
# Single-step build (go fetches + verifies against go.sum) — mirrors the CI
# linux-replay Dockerfile. A separate `go mod download` is avoided: it
# pre-populates the module cache in a way that makes `go build` demand go.sum
# go.mod hashes a clean pruned-graph build never needs.
#
# GOARCH is spelled out in both branches rather than left empty when TARGETARCH
# is: Go reads an empty GOARCH as "unset" and silently falls back to the build
# host's arch, which is right under the classic builder and wrong under a
# BuildKit build whose builder stage is pinned above. Naming GOHOSTARCH says
# which of the two is meant instead of relying on that collapse.
RUN GOARCH="${TARGETARCH:-$(go env GOHOSTARCH)}" \
    go build -trimpath -ldflags "-X main.Version=${VERSION}" \
    -o /out/irrlichtrelay ./cmd/irrlichtrelay

# ---- runtime: the binary + a baked-in dashboard snapshot ----
FROM debian:bookworm-slim AS runtime
# Run as a non-root user rather than the image's root default (docker:S6471)
# — 7839 is unprivileged so this needs no capability grants.
RUN useradd --no-create-home --uid 10001 relay
COPY --from=build /out/irrlichtrelay /usr/local/bin/irrlichtrelay
# Bake the live dashboard so http://<host>:7839 serves it. NOTE: this is a
# snapshot — unlike the daemon's live disk-serve, web edits need an image
# rebuild.
#
# The WHOLE directory, deliberately: this line used to name index.html,
# irrlicht.css and irrlicht.js, while irrlicht.js statically imports ten
# siblings, so the served dashboard 404'd its own module graph and could not
# boot (measured: `GET /collapsedGroups.js` → 404 in the running container).
# It also dropped every Beacon PWA file, leaving the relay serving a push API
# with no client able to consume it (docs/mobile-notifications-arc42.md §8.7,
# risk 6). A directory copy cannot fall behind the way an enumeration does.
# The tests, node_modules and vitest config are kept out of the image by
# /.dockerignore, not by shortening this line; platforms/web/release-files.test.js
# grades both halves.
COPY --chown=relay:relay platforms/web/ /web/
ENV IRRLICHT_UI_DIR=/web
USER relay
EXPOSE 7839
# Bind 0.0.0.0 (NOT the loopback default) so the published port is reachable
# from outside the container; the compose file publishes it on the host
# loopback. Auth/TLS are OFF here — fine for a trusted host. Before exposing on
# a LAN/the internet, add `--auth tokens-file` and front it with TLS (or
# `--tls-cert`/`--tls-key`); see DEPLOY.md.
CMD ["irrlichtrelay", "serve", "--addr", "0.0.0.0:7839"]
