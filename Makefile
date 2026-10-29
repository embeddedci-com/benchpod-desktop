# BenchPod Desktop: build, test and run.
#
#   make local / run-local    the local app: one binary (agent + API + webapp), opens a browser
#   make desktop              the desktop installer around it (needs Rust)
#
# benchpod-ui (the chat and result views) must be checked out next to this repository.

VERSION ?= dev
GO_LDFLAGS := -s -w -X main.version=$(VERSION)
LOCAL_WEB := cmd/benchpod-local/web

.DEFAULT_GOAL := help

.PHONY: help
help:
	@echo "BenchPod Desktop"
	@echo
	@echo "  make local         build bin/benchpod-local: agent, API and webapp in one binary"
	@echo "  make run-local     build and run it (opens a browser)"
	@echo "  make dev           benchpod-local + the webapp with hot reload on :5176"
	@echo "  make desktop       build the desktop app installer for this machine (needs Rust)"
	@echo "  make desktop-dev   run the desktop app in development mode"
	@echo "  make test          go test + the webapp typecheck + benchpod-ui tests"

.PHONY: check-bpui
check-bpui:
	@test -d ../benchpod-ui/src || { echo "benchpod-ui is not checked out next to this repository: git clone https://github.com/embeddedci-com/benchpod-ui ../benchpod-ui"; exit 1; }

.PHONY: local-webapp local run-local dev test
local-webapp: check-bpui
	cd ../benchpod-ui && ([ -d node_modules ] || npm ci)
	cd webapp && ([ -d node_modules ] || npm ci) && VERSION=$(VERSION) npm run build
	find $(LOCAL_WEB) -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
	cp -R webapp/dist-app/. $(LOCAL_WEB)/

local: local-webapp
	CGO_ENABLED=0 go build -trimpath -ldflags "$(GO_LDFLAGS)" -o bin/benchpod-local ./cmd/benchpod-local
	@echo "built bin/benchpod-local"

run-local: local
	./bin/benchpod-local

dev: check-bpui
	go run ./cmd/benchpod-local -open=false & \
	cd webapp && ([ -d node_modules ] || npm ci) && npx vite --open

test: check-bpui
	go vet ./... && go test ./...
	cd webapp && ([ -d node_modules ] || npm ci) && npm run typecheck
	cd ../benchpod-ui && ([ -d node_modules ] || npm ci) && npm test

# ---- the desktop app (Tauri around the benchpod-local sidecar) ----

RUST_TARGET ?= $(shell rustc -vV 2>/dev/null | sed -n 's/^host: //p')
SIDECAR_EXT := $(if $(findstring windows,$(RUST_TARGET)),.exe,)

.PHONY: desktop desktop-dev desktop-sidecar
desktop-sidecar: local-webapp
	@test -n "$(RUST_TARGET)" || { echo "Rust is not installed: https://rustup.rs"; exit 1; }
	mkdir -p desktop/src-tauri/binaries
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(GO_LDFLAGS)" \
		-o desktop/src-tauri/binaries/benchpod-local-$(RUST_TARGET)$(SIDECAR_EXT) ./cmd/benchpod-local

desktop: desktop-sidecar
	cd desktop && ([ -d node_modules ] || npm ci) && npx tauri build $(TAURI_ARGS)

desktop-dev: desktop-sidecar
	cd desktop && ([ -d node_modules ] || npm ci) && npx tauri dev
