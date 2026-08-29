# A container that runs the e2e suite's HEADLESS path.
#
# Why this exists: the default path is `wails dev`, and on macOS every backend
# it starts calls sysProv.Probe (internal/app/app.go) — which the source itself
# describes as "a real keychain write". `wails dev` rebuilds and re-signs the
# binary on every run, so macOS re-asks for keychain access each time, and a
# suite that restarts the backend per spec asks continuously. e2e/home-isolation
# .ts redirects $HOME and names the three things that covers; the login keychain
# is a fourth and it is not one of them.
#
# A Linux container has no macOS keychain to ask, so the boundary is the
# container itself rather than a variable a caller could forget. It also runs
# the path AGENTS.md already documents — cmd/nocx-server plus the discovery-socket
# shim: the real Go backend and a real PTY, with no wails, no GTK and no
# display.
#
# What it does NOT replace: WKWebView. The webkit project here is Linux WebKit,
# which is what CI's e2e job drives too, but the shipped app is macOS WKWebView.
# Anything about the real webview still has to be seen on a Mac.
#
# Usage: e2e/run-in-container.sh [playwright args...]

# ARCHITECTURE: the host's, deliberately — and this is the opposite call from
# .githooks/images/ci-linux/Dockerfile, which pins --platform=linux/amd64 to BE
# the runner. Stating it because a reader comparing the two files has to be able
# to tell a decision from an omission.
#
# So this image is linux/arm64 on an Apple Silicon developer machine and
# linux/amd64 on ubuntu-latest. The trade is measured rather than assumed: at
# the same tree, the local container reported 13 failures and CI 12, and the
# deterministic core — five specs, both engines — was IDENTICAL (nocx-cbtc).
# What made the two runs disagree was never the architecture; it was core count,
# which is why run-in-container.sh now defaults to the runner's 4.
#
# Pinning amd64 here would run two browser engines under QEMU on every developer
# machine this product is built on. The ci-linux image can afford that — it runs
# Go tests, and being the runner is its entire purpose. A Playwright suite cannot:
# an 11-minute gate that becomes an hours-long one is a gate nobody runs, and a
# gate nobody runs is the divergence this whole directory exists to remove.
#
# Revisit if a failure ever turns out to be arch-specific. Nothing has been.
FROM mcr.microsoft.com/playwright:v1.61.1-noble

# Go, pinned to go.mod. The suite compiles cmd/nocx-server and cmd/e2e-sshd.
ARG GO_VERSION=1.26.5
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in \
      amd64) goarch=amd64 ;; \
      arm64) goarch=arm64 ;; \
      *) echo "unsupported arch $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz" -o /tmp/go.tgz; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz
ENV PATH="/usr/local/go/bin:/root/go/bin:${PATH}"
ENV GOTOOLCHAIN=local

# The shells the integration tests drive. They FAIL rather than skip when the
# shell they exist to prove is absent (nocx-gd84), so a thin image would report
# a broken product instead of a missing package — the same reason ci.yml
# installs dash on macOS and zsh on ubuntu.
#
# make, because the stand builds the remote helper's artifacts by calling
# `make helpers` on its way up (e2e/stand.ts) rather than keeping a second
# copy of that build matrix. Without it the suite dies in globalSetup.
RUN apt-get update -qq \
    && apt-get install -y -qq --no-install-recommends bash dash make zsh openssh-client \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /work
