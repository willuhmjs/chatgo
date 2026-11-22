# ChatGo — multi-stage build: node (SPA) → go (static binary) → distroless.
# Keep in sync with docs/CONTRACTS.md §Build (vite outDir = internal/web/dist).
#
# All bases are pinned by image index digest for reproducible builds.
# Bump deliberately (same PR as any dependency change) by re-resolving:
#   docker buildx imagetools inspect <image>:<tag>   # → "Digest:" line

# ---- Stage 1: web assets ---------------------------------------------------
# node:22-alpine — matches CI (setup-node node-version: 22) and package.json engines.
# digest: sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a (node:22-alpine, recorded 2026-11-22)
FROM node:22-alpine@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a AS web
# WORKDIR must be /src/web so vite's outDir "../internal/web/dist" resolves to
# /src/internal/web/dist (the previous WORKDIR /src flattened web/ into /src
# and the bundle landed at /internal/web/dist instead — build-stage copy failed).
WORKDIR /src/web
# Lockfile-only layer first for build-cache friendliness.
# --ignore-scripts: npm ci must not run arbitrary lifecycle scripts from the
# lockfile. Safe here: the only packages with install scripts in this tree are
# esbuild/fsevents — esbuild resolves its binary from the optional platform
# package (@esbuild/linux-x64-musl) at runtime, fsevents is macOS-only and not
# used by `vite build` on linux. Verified 2026-11-22: full `docker build` and
# `svelte-check` both pass with the flag (only install scripts skipped are
# esbuild's download-fallback and fsevents' build).
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
# vite.config.ts writes the bundle to ../internal/web/dist (= /src/internal/web/dist)
RUN npm run build

# ---- Stage 2: Go build ------------------------------------------------------
# golang:1.27-alpine — go.mod pins go 1.27.x.
# digest: sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 (golang:1.27-alpine, recorded 2026-11-22)
FROM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
# modernc.org/sqlite is pure Go → CGO not required; keep the binary static.
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
# Overlay the freshly built SPA on top of the committed embed placeholder.
COPY --from=web /src/internal/web/dist ./internal/web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/chatgo ./cmd/chatgo

# ---- Stage 3: /data bootstrap ----------------------------------------------
# The final image runs as uid 65532 (distroless "nonroot") and expects DATA_DIR
# at /data. A *fresh* named volume (`docker run -v chatgo-data:/data`) is
# initialized from the image's /data — if the path does not exist in the image
# it is created root-owned and the app cannot write its SQLite DB. This stage
# bakes an empty /data owned by 65532:65532. (Busybox COPY needs at least one
# file — /data/.keep is that marker; the app ignores unknown entries.)
# digest: sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec (busybox:1.37, recorded 2026-11-22)
FROM busybox:1.37@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec AS vol
RUN mkdir -p /data && touch /data/.keep && chown -R 65532:65532 /data && chmod 0750 /data

# ---- Stage 4: runtime --------------------------------------------------------
# No HEALTHCHECK directive: distroless has no shell to run curl/wget from;
# liveness/readiness are covered by k8s probes on /healthz (see deploy/).
# digest: sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931 (gcr.io/distroless/static:nonroot, recorded 2026-11-22)
FROM gcr.io/distroless/static:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/chatgo /chatgo
# Order matters: bake writable /data BEFORE switching to nonroot so a fresh
# named volume inherits 65532:65532. Bind-mounts still need host-side perms.
COPY --from=vol --chown=65532:65532 /data /data
USER nonroot:nonroot
EXPOSE 8080
ENV DATA_DIR=/data
ENTRYPOINT ["/chatgo"]
