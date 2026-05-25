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

# ---- runtime: the binary + a baked-in dashboard snapshot ----
FROM debian:bookworm-slim AS runtime
COPY --from=build /out/irrlichtrelay /usr/local/bin/irrlichtrelay
# Bake the live dashboard so http://<host>:7839 serves it. NOTE: this is a
# snapshot — unlike the daemon's live disk-serve, web edits need an image
# rebuild. Only the three runtime files (no tests / node_modules).
COPY platforms/web/index.html platforms/web/irrlicht.css platforms/web/irrlicht.js /web/
ENV IRRLICHT_UI_DIR=/web
EXPOSE 7839
# Bind 0.0.0.0 (NOT the loopback default) so the published port is reachable
# from outside the container. v0 relay has no auth/TLS — only publish it to a
# trusted host loopback.
CMD ["irrlichtrelay", "serve", "--addr", "0.0.0.0:7839"]
