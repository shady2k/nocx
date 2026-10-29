#!/usr/bin/env bash
# Focused release tooling checks, also callable through nocx-sky-test.
set -euo pipefail
node --test scripts/publish-nightly.test.mjs scripts/build-identity.test.mjs scripts/product-change.test.mjs
go test ./internal/version -count=1
python3 -m unittest discover -s scripts -p 'verify_built_artifact_test.py'
node --test .githooks/check-log-context.test.mjs
