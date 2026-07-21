.PHONY: all build dev lint format test clean hooks ci lint-ci test-ci build-ci frontend-ci

GO ?= go
GOFUMPT ?= gofumpt
GOLANGCI_LINT ?= golangci-lint
WAILS ?= wails

all: lint test build

build:
	$(WAILS) build

dev:
	$(WAILS) dev

lint:
	$(GOLANGCI_LINT) run ./...

format:
	$(GOFUMPT) -l -w .

test:
	$(GO) test -v -race -count=1 ./...

clean:
	$(GO) clean -cache
	rm -rf build/

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
