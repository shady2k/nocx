.PHONY: all init build dev dev-web lint format test clean hooks ci ci-full \
        ci-backend ci-linux ci-mac ci-os-split ci-frontend ci-e2e \
        print-os-pkgs print-portable-pkgs \
        lint-ci test-ci build-ci root-ci frontend-ci

GO ?= go
GOFUMPT ?= gofumpt
GOLANGCI_LINT ?= golangci-lint
PKG_CONFIG ?= pkg-config

# The Linux build targets webkit2gtk-4.1, the surface ADR-0007 decided for
# this product. Wails v3 defaults to GTK4/WebKitGTK-6.0; the `gtk3` build tag
# selects its WebKitGTK-4.1 path, which is the 4.1 surface this repo already
# ships. It keeps the same nocx-v3yw rationale as the old webkit2_41 tag: a
# build against an installed 4.1 surface rather than a default that may not
# match the distribution. When 4.1 is absent (a GTK4-only host), the tag is
# empty and v3's GTK4 default is used.
HOST_GOOS ?= $(shell $(GO) env GOOS)
WAILS_PLATFORM_TAGS := $(shell if [ "$(HOST_GOOS)" = "linux" ] && $(PKG_CONFIG) --exists webkit2gtk-4.1 2>/dev/null; then printf gtk3; fi)

# v3 dropped the `wails build` wrapper: the project builds with plain go
# build. Wails v2 used to run the frontend build (wails.json frontend:build);
# the repository now owns that step, because //go:embed all:frontend/dist
# requires a populated dist before the Go compiler runs.
FRONTEND_BUILD := cd frontend && npm run build

# BUILD METADATA, stamped at link time so the About page (nocx-8bbp) reads it
# out of the binary rather than out of a constant somebody has to remember to
# bump. The -X paths are documented in internal/version/version.go, which is the
# source of truth for them; the release workflow builds the same three.
#
# VERSION IS NOT SET HERE, AND THAT IS THE POINT. `internal/version.Version`
# defaults to "dev", and the updater treats that exact string as "this is a
# development build, never check for updates". A local build that stamped a
# version guessed from `git describe` would be a build that offers to replace
# itself with a release. Commit and date are honest about any build, so they
# are always stamped; pass VERSION explicitly to make a build that claims to be
# a release:
#
#   make build-release VERSION=0.3.0
#
# `git describe` is not used even then. The release number is the tag the
# workflow was triggered by, and deriving it locally would be a second answer to
# a question the release pipeline already answers.
VERSION ?=
BUILD_COMMIT := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_PKG := github.com/shady2k/nocx/internal/version
LDFLAGS := -X $(VERSION_PKG).Commit=$(BUILD_COMMIT) -X $(VERSION_PKG).Date=$(BUILD_DATE)
ifneq ($(VERSION),)
LDFLAGS += -X $(VERSION_PKG).Version=$(VERSION)
endif

all: lint test build

# A local build is a DEVELOPMENT build: it resolves the nocx-dev profile, so it
# cannot read or clobber the documents an installed nocx owns
# (internal/storage/appdir.go). Use build-release to produce the shipped
# artefact; CI does that from a tag.
build:
	$(FRONTEND_BUILD)
	$(GO) build $(if $(WAILS_PLATFORM_TAGS),-tags "$(WAILS_PLATFORM_TAGS)") -ldflags "$(LDFLAGS)" -o build/bin/nocx .

# The shipped artefact. `-tags release` is what selects the real profile
# directory, and it is deliberately the side that needs the flag: a build made
# without it costs a developer an empty profile, never a user their data.
# `production` is v3's tag for production build semantics (devtools off).
build-release:
	$(FRONTEND_BUILD)
	$(GO) build -tags "$(strip release production $(WAILS_PLATFORM_TAGS))" -ldflags "$(LDFLAGS)" -o build/bin/nocx .

# A local dev loop: build the embedded frontend and run the app with the dev
# profile. `wails dev` used to provide a hot-reloading asset server; v3 has
# no dev CLI without a Taskfile, and replicating the watcher is not worth
# inventing one for — the dev-web target is the iteration path for frontend
# work, this target is for exercising the real shell.
dev:
	$(FRONTEND_BUILD)
	$(GO) run -tags "$(strip $(WAILS_PLATFORM_TAGS))" .


# The same app in an ordinary browser instead of the Wails webview: backend
# (cmd/devharness, real PTY) plus vite with the Wails bindings shimmed. Needs no
# display, no GTK — forward both ports over SSH and open http://localhost:5180.
# Ports: NOCX_WS_PORT=9880, NOCX_WEB_PORT=5180. Neither is shared with anything
# else: 5173 belongs to `npm run dev` and the e2e suite, 9876 to the e2e suite's
# devharness, 34115 to `wails dev`.
dev-web:
	./scripts/dev-web.sh

lint:
	$(GOLANGCI_LINT) run $(if $(WAILS_PLATFORM_TAGS),--build-tags "$(WAILS_PLATFORM_TAGS)") ./...

format:
	$(GOFUMPT) -l -w .

test:
	$(GO) test -v -race -count=1 $(if $(WAILS_PLATFORM_TAGS),-tags "$(WAILS_PLATFORM_TAGS)") ./...
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
	@echo "Ready. Run 'make dev' to start the app, 'bd ready' for the backlog."

# Per-clone git configuration. Both lines are the same kind of thing: git
# behaviour this repo needs that a clone cannot carry by itself.
#
# The merge driver resolves `.beads/issues.jsonl` by regenerating it from the
# issue database instead of asking which side to keep — see .gitattributes for
# why neither side is ever the answer.
hooks:
	git config core.hooksPath .githooks
	git config merge.beads-export.name "regenerate the beads export from the issue database"
	git config merge.beads-export.driver "bd export -o %A"
	@echo "git hooks installed from .githooks/"

# `ci` is the HOST-SIDE half of CI: the `backend` job (macos-latest) plus the
# host's copy of the `frontend` job. It is the fast gate, and it is deliberately
# NOT the whole matrix — read `ci-full` below before you treat a green `ci` as a
# green run.
#
# ci.yml's header used to claim this target "mirrors the same set of checks so
# green is identical locally and in CI". It did not, and every gap had already
# produced a red run from a gate that had just reported green:
#
#   backend-linux (both keyring variants)  scripts/ci-linux.sh   (nocx-cn86)
#   e2e                                    e2e/run-in-container.sh
#   frontend on the runner's node 24       scripts/ci-frontend.sh
#   the repo-root gates and spec coverage  scripts/ci-frontend.sh (nocx-z9s9.8)
#   go test -tags release ./internal/storage/...   added below
#
# `ci-full` runs all of them, each in the environment its job runs in.
ci: lint-ci test-ci build-ci root-ci frontend-ci
	@echo ""
	@echo "=== host-side gates green ==="
	@echo "NOT covered by this target: backend-linux, e2e, and the frontend job"
	@echo "on the runner's node — those are the other three of 'make ci-full'."

# Every CI job, each through the runner it actually runs on. The containerized
# targets are byte-for-byte their CI counterparts; `ci` is the macOS-only part,
# which is the one job that cannot be containerized because macos-latest is the
# target OS (see ci.yml's runner decision).
#
#   ci + ci-mac               ci.yml `ci-mac`     (macOS, native)
#   ci-backend                ci.yml `ci-backend`
#   ci-linux                  ci.yml `ci-linux`
#   ci-frontend               ci.yml `ci-frontend`
#   ci-e2e                    ci.yml `ci-e2e`
#
# CI-BACKEND IS IN THIS LIST, and its absence is what made this target lie.
# 9527464 narrowed `ci-linux` from "the backend-linux job" to "the eight
# OS-specific packages" and moved the portable suite into the new `ci-backend`
# — but left this line alone. The name survived the change of meaning, so the
# composite went on reading as correct while running none of the portable Go
# suite on Linux at all. Every target it invoked was green, which is exactly
# how a gate that does not run a job reports that the job passed — the defect
# ci-full exists to prevent (nocx-cn86, nocx-1e7x, nocx-aruz).
#
# ci-os-split runs FIRST and costs seconds: it re-derives the OS package list
# from the build constraints, so the partition below cannot drift silently
# into dropping a package from both halves.
#
# ci-mac IS in this list now. It used to be excluded on the grounds that no CI
# job corresponded to it — macos-latest ran the whole suite as `backend` — and
# that is no longer true: ci.yml's ci-mac job is this target's package set, so
# leaving it out would reopen exactly the hole nocx-aruz was about. The
# keychain caveat stands and is stated by the target itself; it applied to
# `make ci` all along, which has always run the Darwin suite.
#
# `ci` stays here for ci.yml's ci-mac job MINUS its test step: gofumpt,
# golangci-lint and the build, which must run on Darwin to see the
# darwin-tagged files at all, plus the host's copy of the frontend gates. Its
# own `go test ./...` is a superset of what the job runs and is left as it is —
# a local gate running more than CI costs minutes, never a hole.
#
# Order is cheapest-first: the drift check in seconds, the host gates next,
# the Linux containers in minutes, e2e last because it is the longest.
ci-full: ci-os-split ci ci-mac ci-backend ci-linux ci-frontend ci-e2e
	@echo ""
	@echo "=== every CI job green locally ==="

# --- the five jobs ------------------------------------------------------
#
# ci-backend  the portable Go suite            Linux container
# ci-linux    the Linux-only packages          Linux container
# ci-mac      the macOS-only packages          NATIVE, run by hand
# ci-frontend node 24                          container
# ci-e2e      playwright                       container
#
# A package is OS-SPECIFIC when its non-test source carries a build
# constraint — mechanical, checkable, and `make ci-os-split` re-derives the
# list and fails if this one has drifted. internal/pty is the one entry not
# derived that way: its source is portable (creack/pty) and its BEHAVIOUR is
# the platform's, which is why it hangs on a macOS workstation while green on
# the runner (nocx-58gq).
#
# ci-backend and ci-linux partition ./... exactly — nothing is dropped by the
# split and nothing is run twice. The point of splitting them is that a red
# run says whether portable behaviour broke or platform behaviour did, which
# one job answering for both could never say.
#
# The keyring matrix rides with ci-linux and NOT with the OS split, because it
# is a fixture dimension rather than a platform one: internal/vault/system is
# the Secret Service binding and lives in the OS set, but portable packages
# read through it too, so both variants run over the whole partition.
# internal/app and internal/ssh/mux joined on 2026-08-21 (nocx-m8jwn.4), and
# ci-os-split is what noticed rather than anybody remembering. The typed-`ssh`
# wrapper gave each of them a `unix` / `!unix` pair — the multiplex socket's
# SCM_RIGHTS descriptor passing, and the probes that decide a refusal class —
# and a package with a platform split that stays in the portable set has its
# `!unix` half compiled by nothing at all.
OS_PKG_DIRS := cmd/e2e-sshd internal/apicoll internal/app internal/contentkey \
               internal/lifecyclechannel \
               internal/loginshell internal/nativeports internal/procwatch \
               internal/pty internal/reveal internal/ssh/mux \
               internal/storage internal/update internal/vault/system
OS_PKG_RE := (cmd/e2e-sshd|internal/apicoll|internal/app|internal/contentkey|internal/lifecyclechannel|internal/loginshell|internal/nativeports|internal/procwatch|internal/pty|internal/reveal|internal/ssh/mux|internal/storage|internal/update|internal/vault/system)
OS_PKGS := $(addprefix ./,$(addsuffix /...,$(OS_PKG_DIRS)))

# BOTH keyring variants here too, and the comment above already said so —
# "both variants run over the whole partition" — while the recipe passed
# --no-keyring and ran the portable half once. CI's backend-linux runs
# `go test ./...` in each variant, so a portable package that reads through
# the Secret Service binding is exercised there with a keyring present and was
# not exercised that way here. The two halves are a PLATFORM split; the
# keyring is a fixture dimension that crosses both (nocx-aruz).
ci-backend:
	@echo "=== ci-backend: the portable half of ci.yml's backend-linux job ==="
	./scripts/ci-linux.sh -- $$($(GO) list ./... | grep -vE 'nocx/$(OS_PKG_RE)(/|$$)')

ci-linux:
	@echo "=== ci-linux: the OS-specific half of ci.yml's backend-linux job ==="
	./scripts/ci-linux.sh -- $(OS_PKGS)

# ci-os-split re-derives the OS package list from the build constraints and
# fails when OS_PKG_DIRS has drifted from it. Without this the list is a
# hand-kept copy of a fact the compiler already knows, and the first package
# to grow a _darwin.go would quietly stop being covered by ci-mac.
#
# A GOOS, not merely a build line. The first version asked only whether a
# package had a `//go:build` at all, which cannot tell `//go:build linux` from
# `//go:build release` — and the repo already contained both. It went unseen
# because the check ran in no composite: wiring it into ci-full flagged
# internal/log (release/!release, added by bea5b6f) on the first run, while
# internal/storage — the SAME constraint shape — sat inside OS_PKG_DIRS
# unflagged. Two packages, one rule, opposite verdicts (nocx-aruz).
GOOS_RE := (aix|android|darwin|dragonfly|freebsd|hurd|illumos|ios|js|linux|netbsd|openbsd|plan9|solaris|wasip1|windows|unix)

# In the OS set although no build line names a GOOS. The derivation cannot
# supply a reason, so each one is stated here:
#
#   internal/pty      portable source (creack/pty) whose BEHAVIOUR is the
#                     platform's — it hangs on a macOS workstation while green
#                     on the runner (nocx-58gq).
#   internal/storage  the shipped profile directory is behind `-tags release`
#                     (appdir.go). Not a GOOS, but it is the one package whose
#                     tested code differs between what a developer builds and
#                     what ships, and ci-mac is what runs the release-tag pass.
# internal/shellintegration is NOT here, and the reason is worth stating
# because it is the package you would expect to be. Its behaviour is the
# platform's shell — macOS ships GNU bash 3.2.57, where a bash-4 construct is a
# syntax error at PARSE time (nocx-cn86) — but that dimension is a BASH
# VERSION, not an operating system, and scripts/install-bash32.sh puts a real
# 3.2 on the Linux runner. requireBash32 resolves it there and /bin/bash on a
# Mac, so TestBashScript_ParsesUnderBash32 and the channel-exec bash32 leg
# measure the same shell on either side. Keeping the package on macos-latest
# would buy the OS around the shell, not the shell.
OS_EXEMPT := internal/pty internal/storage

# The workflow asks for the split rather than carrying a copy of it. ci.yml's
# ci-backend and ci-linux jobs need the same two package sets these targets
# use, and a list written twice is two lists: the day one grows a package the
# other does not, a package silently runs nowhere or twice and both files
# still read as correct. One owner, asked over `make -s` (AD-8).
print-os-pkgs:
	@echo '$(OS_PKGS)'

print-portable-pkgs:
	@$(GO) list ./... | grep -vE 'nocx/$(OS_PKG_RE)(/|$$)'

ci-os-split:
	@echo "=== the OS split is derived from the build constraints, not remembered ==="
	@derived=$$(grep -rlE '^//go:build.*$(GOOS_RE)' --include='*.go' \
	  --exclude-dir=node_modules --exclude-dir=worktrees . \
	  | grep -v '_test\.go$$' | xargs -n1 dirname | sed 's|^\./||' | sort -u \
	  | tr '\n' ' '); \
	missing=""; \
	for d in $$derived; do \
	  case " $(OS_PKG_DIRS) " in *" $$d "*) ;; *) missing="$$missing $$d";; esac; \
	done; \
	extra=""; \
	for d in $(OS_PKG_DIRS); do \
	  case " $$derived " in *" $$d "*) ;; *) \
	    case " $(OS_EXEMPT) " in *" $$d "*) ;; *) extra="$$extra $$d";; esac ;; \
	  esac; \
	done; \
	rc=0; \
	if [ -n "$$missing" ]; then \
	  echo "FAIL: names a GOOS but is not in OS_PKG_DIRS:$$missing"; rc=1; fi; \
	if [ -n "$$extra" ]; then \
	  echo "FAIL: in OS_PKG_DIRS, names no GOOS, and is not in OS_EXEMPT:$$extra"; rc=1; fi; \
	if [ $$rc = 0 ]; then echo "ok"; fi; \
	exit $$rc

# ci-mac is the ONLY gate with no container, because macos-latest is the
# target OS and Docker on a Mac runs Linux. It is therefore also the only one
# that runs against a real machine with a real login keychain, so it is NOT in
# `make ci` — you run it by hand, when a Darwin-specific failure needs
# reproducing.
#
# Hermetic by construction, and it refuses to start otherwise. The three
# variables below move the profile directory, the home directory and the
# temporary root off the developer's own; without them this suite spawns real
# shells that read the developer's rc files and writes where the developer
# lives. That is not hypothetical — an e2e run once reset this developer's
# settings and theme on every pass (nocx-ti8w), and a shell test never reached
# prompt_ready here while passing on an empty runner because ~/.bashrc loads a
# second prompt integration (nocx-58gq).
#
# The keychain is the one thing a directory cannot move: go-keyring talks to
# the Keychain service, and app.New probes the system vault provider on every
# backend start. That probe is a real keychain write and it is stated here
# rather than pretended away.
# The teardown is the dangerous half of this target, so it is written to be
# incapable of the accident rather than merely unlikely to have it.
#
# It used to be `trap 'rm -rf "$$root"' EXIT`, with $$root expanded when the
# trap FIRES. Everything then rests on that one variable still holding what
# mktemp returned: an `rm -rf` under a HOME this recipe itself redirects is one
# unset variable away from `rm -rf ""` — and one mistaken assignment away from
# something much worse. Nothing checked it, and the target's whole job is to
# delete a directory tree.
#
# So the path is checked against the temporary root it must live under, at
# creation AND again inside the trap, and rm runs only if the pattern holds.
# An unset, empty, relative or reassigned $$root fails the case and the trap
# says so instead of deleting. `rm -rf --` on top, so a path that somehow began
# with a dash could not be read as options.
#
# The chmod is not tidiness: Go makes every file in the module cache
# read-only, and the disposable HOME grew one (GOPATH defaults to $$HOME/go),
# so the old teardown died with a screenful of "Permission denied" and left
# the tree behind on every run. GOMODCACHE and GOCACHE now point at the host's
# real caches — they are build artefacts, not the user's documents, and they
# are not what a disposable root exists to protect — so nothing read-only
# lands inside it at all. The chmod stays as the belt to that braces.
ci-mac:
	@echo "=== ci-mac: the OS-specific packages, natively, in a disposable root ==="
	@set -eu; \
	  tmpbase="$${TMPDIR:-/tmp}"; tmpbase="$${tmpbase%/}"; \
	  case "$$tmpbase" in \
	    /*) ;; \
	    *) echo "ci-mac: TMPDIR '$$tmpbase' is not absolute — refusing to run" >&2; exit 1 ;; \
	  esac; \
	  root="$$(mktemp -d "$$tmpbase/nocx-ci-mac.XXXXXX")"; \
	  case "$$root" in \
	    "$$tmpbase"/nocx-ci-mac.??????*) ;; \
	    *) echo "ci-mac: mktemp produced '$$root', which is not under '$$tmpbase' — refusing to run" >&2; exit 1 ;; \
	  esac; \
	  echo "disposable root: $$root"; \
	  gomodcache="$$($(GO) env GOMODCACHE)"; gocache="$$($(GO) env GOCACHE)"; \
	  cleanup() { \
	    case "$${root:-}" in \
	      "$$tmpbase"/nocx-ci-mac.??????*) \
	        chmod -R u+w "$$root" 2>/dev/null || true; \
	        rm -rf -- "$$root" ;; \
	      *) echo "ci-mac: refusing to remove '$${root:-<unset>}' — not under '$$tmpbase'" >&2 ;; \
	    esac; \
	  }; \
	  trap cleanup EXIT INT TERM; \
	  NOCX_TEST_APP_DIR="$$root/profile" HOME="$$root/home" TMPDIR="$$root/tmp" \
	  GOMODCACHE="$$gomodcache" GOCACHE="$$gocache" \
	  sh -c 'mkdir -p "$$NOCX_TEST_APP_DIR" "$$HOME" "$$TMPDIR" && \
	         $(GO) test -race -count=1 $(OS_PKGS) && \
	         echo "" && \
	         echo "=== the shipped profile directory (-tags release) ===" && \
	         $(GO) test -race -count=1 -tags release ./internal/storage/...'
	@echo ""
	@echo "=== ci-mac green — NOTE: the login keychain is shared with your Mac"
	@echo "    and is the one thing a disposable root cannot isolate. ==="

ci-frontend:
	@echo "=== ci-frontend: ci.yml's frontend job, on the runner's node 24 ==="
	./scripts/ci-frontend.sh

ci-e2e:
	@echo "=== ci-e2e: ci.yml's e2e jobs, the same image and command ==="
	@echo "    (CI runs one job per browser in parallel; this runs both in sequence)"
	./e2e/run-in-container.sh

lint-ci:
	@echo "=== gofumpt check ==="
	$(GOFUMPT) -l .
	@test -z "$$($(GOFUMPT) -l .)" || (echo "FAIL: files need formatting" && exit 1)
	@echo ""
	@echo "=== golangci-lint ==="
	$(GOLANGCI_LINT) run $(if $(WAILS_PLATFORM_TAGS),--build-tags "$(WAILS_PLATFORM_TAGS)") ./...

test-ci:
	@echo "=== go test -race ==="
	$(GO) test -race -count=1 $(if $(WAILS_PLATFORM_TAGS),-tags "$(WAILS_PLATFORM_TAGS)") ./...
	@echo ""
	@echo "=== go test -race -tags release (the shipped profile directory) ==="
	@# The shipped profile directory lives behind `-tags release`
	@# (internal/storage/appdir.go), so the ordinary run never compiles it. The
	@# `backend` job runs this; this target did not, which is exactly the kind
	@# of gap that makes a green local gate mean nothing.
	$(GO) test -race -count=1 -tags release ./internal/storage/...

build-ci:
	@echo "=== go build ./... ==="
	$(GO) build $(if $(WAILS_PLATFORM_TAGS),-tags "$(WAILS_PLATFORM_TAGS)") ./...

# The repo-root gates: a different eslint config and a different tree from
# frontend/'s, covering e2e/, the hooks and the config files. They ran ONLY in
# the pre-commit hook, where they had been dying on EACCES against the e2e
# container's root-owned output — and a crashing gate reports nothing, so 19
# lint errors and 15 unformatted files accumulated behind it before CI grew
# steps for them (nocx-z9s9.8). Nothing local ran them until now.
root-ci:
	@echo "=== repo root (e2e, hooks, config) ==="
	@if [ ! -d node_modules ]; then echo "FAIL: node_modules not found — run 'npm ci' first"; exit 1; fi
	@echo "--- tsc --noEmit (the e2e suite) ---"
	npm run typecheck
	@echo "--- eslint ---"
	npm run lint
	@echo "--- prettier check ---"
	npm run format:check
	@echo "--- every spec file is collected ---"
	node e2e/check-coverage.mjs

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
