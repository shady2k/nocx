#!/usr/bin/env bash
# Read tags from stdin; ignore moving/dated nightlies and prerelease versions.
set -euo pipefail
grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sed 's/^v//' | sort -t. -k1,1n -k2,2n -k3,3n | tail -1 | sed 's/^/v/'
