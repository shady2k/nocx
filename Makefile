.PHONY: all init build dev dev-web lint format test clean hooks ci lint-ci test-ci build-ci frontend-ci

GO ?= go
GOFUMPT ?= gofumpt
GOLANGCI_LINT ?= golangci-lint
WAILS ?= wails

all: lint test build

# A local build is a DEVELOPMENT build: it resolves the nocx-dev profile, so it
# cannot read or clobber the documents an installed nocx owns
# (internal/storage/appdir.go). Use build-release to produce the shipped
# artefact; CI does that from a tag.
build:
	$(WAILS) build

# The shipped artefact. `-tags release` is what selects the real profile
# directory, and it is deliberately the side that needs the flag: a build made
# without it costs a developer an empty profile, never a user their data.
build-release:
	$(WAILS) build -tags release

dev:
	$(WAILS) dev

# The same app in an ordinary browser instead of the Wails webview: backend
# (cmd/devharness, real PTY) plus vite with the Wails bindings shimmed. Needs no
# display, no GTK — forward both ports over SSH and open http://localhost:5180.
# Ports: NOCX_WS_PORT=9880, NOCX_WEB_PORT=5180. Neither is shared with anything
# else: 5173 belongs to `npm run dev` and the e2e suite, 9876 to the e2e suite's
# devharness, 34115 to `wails dev`.
dev-web:
	./scripts/dev-web.sh

lint:
	$(GOLANGCI_LINT) run ./...

format:
	$(GOFUMPT) -l -w .

test:
	$(GO) test -v -race -count=1 ./...
	@echo "=== go test -tags release (the shipped profile directory) ==="
	$(GO) test -race -count=1 -tags release ./internal/storage/...

# Conformance against the real ssh binary. Skipped by the ordinary suite on
# purpose: it needs an ssh on PATH and reads a config it writes itself, so it
# would fail for anyone without one. It is the ONLY check that proves nocx and
# OpenSSH agree — including on Match, the directive whose mishandling forced
# ADR-0015 — so it has a target rather than living behind an env var nobody
# knows about. Run it when the ssh -G parsing or the resolver changes.
conformance:
	NOCX_TEST_SSH_G=1 $(GO) test -v -count=1 -run Conformance ./internal/ssh/...

clean:
	$(GO) clean -cache
	rm -rf build/

# Everything a fresh clone needs, in one command. Safe to re-run.
#
# The issue database is the part people miss: git carries neither the Dolt
# database nor the ref it lives on, so without bootstrap a clone has no backlog
# at all — `bd ready` just reports that no database was found.
init: hooks
	@if ! command -v bd >/dev/null 2>&1; then \
		echo "=== issue tracker: bd not installed, skipping (see README) ==="; \
	elif bd ready >/dev/null 2>&1; then \
		echo "=== issue tracker: database already present ==="; \
	else \
		echo "=== issue tracker: bootstrapping ==="; \
		bd bootstrap --yes; \
	fi
	@echo "=== e2e dependencies ==="
	npm ci
	@echo "=== frontend dependencies ==="
	cd frontend && npm ci
	@echo ""
	@echo "Ready. Run 'wails dev' to start the app, 'bd ready' for the backlog."

hooks:
	git config core.hooksPath .githooks
	@echo "git hooks installed from .githooks/"

ci: lint-ci test-ci build-ci frontend-ci

lint-ci:
	@echo "=== gofumpt check ==="
	$(GOFUMPT) -l .
	@test -z "$$($(GOFUMPT) -l .)" || (echo "FAIL: files need formatting" && exit 1)
	@echo ""
	@echo "=== golangci-lint ==="
	$(GOLANGCI_LINT) run ./...

test-ci:
	@echo "=== go test -race ==="
	$(GO) test -race -count=1 ./...

build-ci:
	@echo "=== go build ./... ==="
	$(GO) build ./...

frontend-ci:
	@echo "=== frontend ==="
	@if [ ! -d frontend/node_modules ]; then echo "FAIL: frontend/node_modules not found — run 'cd frontend && npm ci' first"; exit 1; fi
	@echo "--- prettier check ---"
	cd frontend && npm run format:check
	@echo "--- eslint ---"
	cd frontend && npm run lint
	@echo "--- tsc --noEmit ---"
	cd frontend && npm run typecheck
	@echo "--- vitest ---"
	cd frontend && npm test
	@echo "--- vite build ---"
	cd frontend && npm run build
