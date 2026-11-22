# ChatGo — multi-stage build: node (SPA) → go (static binary) → distroless.
# Keep in sync with docs/CONTRACTS.md §Build (vite outDir = internal/web/dist).

# ---- Stage 1: web assets -------------------------------------------------
FROM node:26-alpine AS web
WORKDIR /src
# Lockfile-only layer first for build-cache friendliness.
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# vite.config.ts writes the bundle to ../internal/web/dist (= /src/internal/web/dist)
RUN npm run build

# ---- Stage 2: Go build ----------------------------------------------------
FROM golang:1.27-alpine AS build
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

# ---- Stage 3: runtime -----------------------------------------------------
# No HEALTHCHECK directive: distroless has no shell to run curl/wget from;
# liveness/readiness are covered by k8s probes on /healthz (see deploy/).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/chatgo /chatgo
USER nonroot:nonroot
EXPOSE 8080
ENV DATA_DIR=/data
ENTRYPOINT ["/chatgo"]
