# The `backend-linux` CI job, as an image (nocx-cn86).
#
# This is NOT the pre-commit test image. That one is golang:1.26-bookworm and
# exists to be fast and to run offline once warm; this one exists to be the
# same environment as the runner, and it is a different environment in every
# way that has bitten us:
#
#   .githooks/images/go-tests   ubuntu-24.04 (this file)
#   Debian bookworm             Ubuntu 24.04.4 LTS, the runner image
#   Go from the golang image    Go from go.dev, the version setup-go resolves
#   Debian zsh, compaudit as-is Ubuntu zsh, compaudit perms fixed
#   no Secret Service at all    gnome-keyring under dbus-run-session
#   every host core             the runner's core count (see scripts/ci-linux.sh)
#
# A release attempt and a follow-up PR both came back red from a CI job the
# hook had just reported green (2026-08-10). Three tests failed on the runner
# that no local command in the repo exercised the same way, and the keyring
# variant — one of the two the job gates on — had no local equivalent
# whatsoever.
#
# What this image gives up, deliberately: the offline warm run. It fetches a
# Go toolchain and Ubuntu packages when it is built. That is the trade the
# hook image refuses to make and this one has to, because "the same packages
# the runner has" is the entire point. The hook keeps its own image; this one
# is run by hand from scripts/ci-linux.sh before a push that matters.
# See the hook image's Dockerfile: `cp -L` because libncursesw.so.6 is a
# symlink in that image and COPY --from would carry the link, not the file.
# --platform is deliberate, and the opposite call from the go-tests image.
# This image exists to BE the runner: GitHub's ubuntu-latest for a public repo
# is linux/amd64, and the Go toolchain below is fetched as linux-amd64. Left
# to the host it would have become an arm64 Ubuntu holding an amd64 Go on an
# Apple Silicon machine, which is neither the runner nor a working image.
# Emulated and slow on arm64 is the correct trade here: a job that reproduces
# CI is worth more than a fast one that does not.
FROM --platform=linux/amd64 bash:3.2 AS bash32
RUN mkdir -p /out \
 && cp -L /usr/local/bin/bash /lib/ld-musl-x86_64.so.1 /usr/lib/libncursesw.so.6 /out/

FROM --platform=linux/amd64 node:24-bookworm-slim AS node

FROM --platform=linux/amd64 ubuntu:24.04

ARG GO_VERSION=1.26.5

ENV DEBIAN_FRONTEND=noninteractive

# The runner image carries build-essential, git, curl and openssh already; the
# rest is what the job's own steps install (zsh for the launcher tests,
# gnome-keyring + dbus-x11 for the Secret Service variant). dash is Essential
# on Ubuntu as it is on Debian, and named for the same reason the hook image
# names it: the shell tests' contract belongs in one visible place.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl git \
      dash zsh openssh-server openssh-client \
      gnome-keyring dbus-x11 \
      libgtk-3-dev libwebkit2gtk-4.1-dev \
 && rm -rf /var/lib/apt/lists/*

# Node is needed by the real pi connector integration tests.
COPY --from=node /usr/local/ /usr/local/
RUN npm install --global @earendil-works/pi-coding-agent@1.5.2

# setup-go with `go-version: '1.26'` resolves to the newest 1.26.x, which is
# what the job actually runs; GO_VERSION pins it here so a red run can be
# reproduced at the version that produced it.
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz \
 && tar -C /usr/local -xzf /tmp/go.tgz \
 && rm /tmp/go.tgz
ENV PATH=/usr/local/go/bin:/usr/local/bin:$PATH

# The same bash 3.2 the hook image carries, for the same test — the parse
# check has to run in every image that runs the Go suite, or it is a check
# that reports green by being absent. See the hook image's Dockerfile for why
# the binary comes from bash:3.2 rather than from a package or a build.
COPY --from=bash32 /out/ /opt/bash32/
RUN printf '#!/bin/sh\nexec /opt/bash32/ld-musl-x86_64.so.1 --library-path /opt/bash32 /opt/bash32/bash "$@"\n' \
      > /usr/local/bin/bash32 \
 && chmod +x /opt/bash32/bash /usr/local/bin/bash32 \
 && bash32 --version | grep -q 'version 3\.2'

# Ubuntu's zsh ships group-writable completion directories, so compinit asks
# "Ignore insecure directories and continue [y] or abort compinit [n]?" — on a
# tty, interactively. The launcher tests drive a real PTY and nothing answers,
# so a zsh test hangs until its own timeout and reports whatever it was
# waiting for instead of the question it was being asked. Fixed rather than
# suppressed, exactly as the CI job does it: ZSH_DISABLE_COMPFIX would silence
# the prompt and leave the tests running a zsh configured unlike any user's.
RUN zsh -c 'autoload -Uz compaudit; compaudit 2>/dev/null || true' \
      | while read -r d; do [ -n "$d" ] && chmod -R go-w "$d"; done \
 && test -z "$(zsh -c 'autoload -Uz compaudit; compaudit 2>/dev/null || true')"
