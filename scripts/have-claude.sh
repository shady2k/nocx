#!/bin/sh
# Does this host have the real Claude Code CLI and an authenticated session?
#
# Prints the executable path and exits 0 only when both facts are true. A
# missing executable is exit 1; an installed CLI whose authentication status
# is unavailable or logged out is exit 2. The caller can therefore tell a
# missing installation from a login action, and can name the package it did
# not run instead of silently skipping a vendor conformance check.
set -eu

if ! claude_path=$(command -v claude 2>/dev/null); then
    printf '%s\n' 'Claude Code is not installed; install claude before running the vendor conformance check.' >&2
    exit 1
fi

if ! status=$("$claude_path" auth status 2>/dev/null); then
    printf '%s\n' 'Claude Code is installed but authentication status could not be read; run claude auth status.' >&2
    exit 2
fi

case "$status" in
    *'"loggedIn": true'*|*'"loggedIn":true'*)
        printf '%s\n' "$claude_path"
        exit 0
        ;;
    *)
        printf '%s\n' 'Claude Code is installed but not authenticated; run claude auth login.' >&2
        exit 2
        ;;
esac
