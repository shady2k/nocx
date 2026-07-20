.PHONY: all build dev lint format test clean

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
