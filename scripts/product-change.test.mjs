import { test } from 'node:test'
import assert from 'node:assert/strict'
import { isProductFile, touchesProduct } from './product-change.mjs'
test('CI and nightly share the supporting-path exceptions', () => {
  for (const file of [
    '.beads/issues.jsonl',
    'docs/decisions/example.md',
    '.internal/spec.md',
    '.githooks/backlog-gate/check.mjs',
    '.githooks/pre-commit',
    '.githooks/commit-msg',
    '.githooks/pre-merge-commit',
    '.githooks/pre-push',
    'scripts/connect-clone.sh',
    '.githooks/adr-identifier.mjs',
    '.githooks/check-adr-identifiers.test.mjs',
    'README.md',
  ]) {
    assert.equal(isProductFile(file), false, file)
  }
  for (const file of [
    'main.go',
    'frontend/src/main.ts',
    '.github/workflows/ci.yml',
    '.githooks/check-log-context.mjs',
    'scripts/install/install.sh',
    'nested/readme.md',
  ]) {
    assert.equal(isProductFile(file), true, file)
  }
  assert.equal(touchesProduct([]), true)
})

test('release tooling is supporting but CI, stable release and app wiring stay product', () => {
  for (const file of [
    '.github/workflows/nightly.yml',
    '.github/workflows/release-build.yml',
    'scripts/product-change.mjs',
    'scripts/publish-nightly.test.mjs',
    'scripts/verify-built-artifact.py',
    'Casks/nocx.rb',
    'Casks/nocx@nightly.rb',
  ])
    assert.equal(isProductFile(file), false, file)
  for (const file of [
    '.github/workflows/ci.yml',
    '.github/workflows/release.yml',
    'main.go',
    'release_diagnostics.go',
    'scripts/unknown.mjs',
    'frontend/src/main.ts',
  ])
    assert.equal(isProductFile(file), true, file)
})

test('new supporting pipeline files always owe ci-pipeline checks', async () => {
  const { readFileSync } = await import('node:fs')
  const ci = readFileSync(new URL('../.github/workflows/ci.yml', import.meta.url), 'utf8')
  const job = ci.split('  ci-pipeline:')[1].split('  ci-mac:')[0]
  assert.ok(job)
  assert.doesNotMatch(job, /needs:|if:/)
  assert.match(job, /scripts\/release\/\*\.test\.\*/)
  assert.match(job, /scripts\/publish-nightly.test.mjs/)
  for (const f of [
    'scripts/release/promotion.mjs',
    'scripts/release/promotion-github.test.mjs',
    'scripts/release/generate-casks.py',
    'scripts/release/generate-casks.test.mjs',
  ])
    assert.equal(isProductFile(f), false, f)
})
