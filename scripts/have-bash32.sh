#!/bin/sh
# Does this host have a GNU bash 3.2 — the bash macOS ships as /bin/bash, and
# the OLDEST bash this product must work on?
#
# Prints the path and exits 0 when it does; prints nothing and exits 1 when it
# does not. That is the whole contract; nothing here installs anything.
#
# WHO ASKS, AND WHY A GATE HAS TO. internal/shellintegration's suite drives the
# shipped script through that bash and FAILS rather than skips when there is
# none (requireBash32, nocx-cn86) — deliberately, because a skip there is how a
# real 3.2 regression reports green on every Linux machine in the project. So
# on a host without the fixture that package cannot run, and `make test-ci`
# asks this before it decides which packages the HOST leg covers and which the
# containerized leg does. The answer is EVIDENCE — a bash on this machine
# answering `--version` — and not a variable somebody sets, which would be a
# skip with extra steps: it would answer "was this checked" with "somebody said
# so".
#
# ONE QUESTION, TWO READINGS. requireBash32 owns it for the SUITE and this owns
# it for the GATE, because a `go test` helper cannot be asked from a Makefile
# and a shell script cannot be asked from inside a test binary without a fork
# per test. The two are kept in step by TestBash32Probe_AgreesWithTheSuite,
# which runs both against this host and fails if they disagree — the candidate
# list below and `bashCandidates` are one list, checked rather than remembered.
#
# HOW TO GET THE FIXTURE. macOS has it at /bin/bash and needs nothing. On Linux
# it is `bash32`, installed by scripts/install-bash32.sh (which is what the CI
# Linux runner calls) and baked into the container images.
set -eu

for candidate in bash bash32 /bin/bash; do
    path=$(command -v "$candidate" 2>/dev/null) || continue
    version=$("$path" --version 2>/dev/null | head -n 1) || continue
    case "$version" in
        "GNU bash, version 3.2"*)
            printf '%s\n' "$path"
            exit 0
            ;;
    esac
done

exit 1
