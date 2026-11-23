# ChatGo — build/run/lint/test orchestration. See docs/CONTRACTS.md §Build.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BIN     := bin/chatgo
LDFLAGS := -s -w -X main.version=$(VERSION)
IMAGE   ?= chatgo:local                 # local dev tag; CI pushes ghcr.io/<org>/chatgo

# Dev convenience only — never rely on a generated secret in production.
SESSION_SECRET ?= $(shell openssl rand -hex 32)

.PHONY: all build run dev dev-web test lint docker bench clean web

all: build

## build: web assets (npm ci + vite → internal/web/dist) then static Go binary
build: web
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/chatgo

## web: build the Svelte SPA into the go:embed dir (internal/web/dist)
web:
	cd web && npm ci && npm run build

## run: dev server with local data dir (vite runs separately via dev-web)
run:
	DATA_DIR=./data PORT=$(or $(PORT),8080) SESSION_SECRET=$(SESSION_SECRET) \
		ALLOW_LOOPBACK=true go run ./cmd/chatgo

## dev-web: vite dev server (proxies /api /files /v1 → :8080)
dev-web:
	cd web && npm install && npm run dev

## test: Go tests + frontend type/contract check
test:
	go test ./...
	cd web && npm run check

## lint: gofmt cleanliness, go vet, frontend check
lint:
	@unfmt=$$(gofmt -l . | grep -v '^web/'); \
	if [ -n "$$unfmt" ]; then echo "gofmt needed:"; echo "$$unfmt"; exit 1; fi
	go vet ./...
	cd web && npm run check

## docker: build local image (multi-stage → distroless). IMAGE=… to retarget.
docker:
	docker build -t $(IMAGE) --build-arg VERSION=$(VERSION) .

## bench: build binary, boot it against a dummy provider, measure RSS
bench: build
	./bench/measure.sh

## clean: remove build outputs and local data (keeps node_modules)
clean:
	rm -rf bin web/dist data
	rm -rf internal/web/dist
	@# internal/web/dist/index.html is a tracked go:embed placeholder (so
	@# `go build ./...` works pre-npm) — restore it after wiping vite output.
	@mkdir -p internal/web/dist
	@printf '<!doctype html>\n<html lang="en" class="dark" style="background:#0d0d0f">\n  <head>\n    <meta charset="utf-8" />\n    <meta name="viewport" content="width=device-width, initial-scale=1" />\n    <title>ChatGo</title>\n  </head>\n  <body>\n    <div id="app"><p style="color:#9ca3af;font-family:system-ui;padding:2rem">ChatGo — run <code>cd web &amp;&amp; npm install &amp;&amp; npm run build</code> to build the SPA.</p></div>\n  </body>\n</html>\n' > internal/web/dist/index.html

