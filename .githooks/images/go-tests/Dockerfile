# Image the pre-commit hook runs `go test -race ./...` in (go_test_containerized).
#
# The stock golang:1.26-bookworm carries bash and dash (Debian's /bin/sh is
# dash, and dash is an Essential package, so /usr/bin/dash is always present)
# but NOT zsh. The shellintegration launcher tests fail, not skip, when the
# shell they must prove is absent (nocx-gd84) — so a hook that ran the stock
# image would fail the three TestZshLauncher_* tests on every commit.
#
# zsh is installed HERE, in the image, instead of via an apt-get in the run
# command, for a measured reason: an `apt-get update && apt-get install zsh`
# in a fresh golang:1.26-bookworm container costs ~28s (measured 2026-08-04)
# and needs Debian-mirror network on every commit, which would break the
# hook's warm-run-offline property (the image and caches are what make a
# repeat commit run with no network). Building the image instead confines the
# apt cost to first use and to base-image updates. The hook builds this image
# (with the BuildKit layer cache) before every run, so this Dockerfile is the
# source of truth: a package added here takes effect on the next commit
# without anyone remembering to rebuild or re-tag anything.
#
# dash is named explicitly even though Debian's Essential set guarantees it:
# the shell tests' contract is "dash and zsh are present", and pinning both
# here keeps that contract in one visible place instead of inheriting dash by
# accident of the base image's Essential set.
#
# openssh-server: the live-sshd suite (nocx-u7uh.17) proves the remote
# lifecycle channel against a REAL OpenSSH server spawned by the test. The
# stock image carries bash/dash/ssh/ssh-keygen but not sshd; the same
# fail-not-skip rule that put zsh here requires the fixture to be provable
# inside the container the hook runs.
#
# bash32: macOS ships GNU bash 3.2.57 as /bin/bash and this product is
# macOS-first, so 3.2 is the oldest bash the integration script must load in.
# Debian has no 3.2 package and building 3.2.57 from source on a modern
# toolchain does not go in one command, so the binary is taken from the
# official `bash:3.2` image. That image is Alpine, so the binary is
# musl-linked: it and its two shared libraries live under a private prefix
# and are invoked through the musl loader explicitly, because dropping a musl
# libncursesw into /lib would sit next to Debian's glibc copy of the same
# soname. Nothing in the image links against it — it exists to be run by
# TestBashScript_ParsesUnderBash32, which is a `bash -n` parse check
# (nocx-cn86).
# Staged through `cp -L` rather than copied straight out with COPY --from,
# because libncursesw.so.6 is a symlink over there and a COPY of a symlink is
# a symlink — one that points at a path this image does not have.
# The musl loader is named for the architecture — ld-musl-x86_64.so.1 on an
# Intel machine, ld-musl-aarch64.so.1 on Apple Silicon — so it is staged under
# a fixed name instead. Naming it x86_64 made this image unbuildable on an
# arm64 host, which is to say on the developer machines this product targets
# first, and the hook that builds it fails before running a single test.
# Nothing links against the loader by soname; it is only ever exec'd, so the
# rename costs nothing.
FROM bash:3.2 AS bash32
RUN mkdir -p /out \
 && cp -L /usr/local/bin/bash /usr/lib/libncursesw.so.6 /out/ \
 && cp -L /lib/ld-musl-*.so.1 /out/loader

FROM golang:1.26-bookworm

RUN apt-get update \
 && apt-get install -y --no-install-recommends dash zsh openssh-server libgtk-3-dev libwebkit2gtk-4.1-dev \
 && rm -rf /var/lib/apt/lists/*

COPY --from=bash32 /out/ /opt/bash32/
RUN printf '#!/bin/sh\nexec /opt/bash32/loader --library-path /opt/bash32 /opt/bash32/bash "$@"\n' \
      > /usr/local/bin/bash32 \
 && chmod +x /opt/bash32/bash /opt/bash32/loader /usr/local/bin/bash32 \
 && bash32 --version | grep -q 'version 3\.2'
