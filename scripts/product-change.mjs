// One owner for the CI and nightly product/supporting predicate (ADR-0097).
// Owner decision 2026-10-27: release tooling owes its own workflow/fixture
// checks, not the product suites it is replacing. Keep exact paths here;
// ci.yml/release.yml and product Go entrypoints intentionally stay product.
const pipelineFiles = new Set([
  '.github/workflows/nightly.yml',
  '.github/workflows/release-build.yml',
  'scripts/product-change.mjs',
  'scripts/product-change.test.mjs',
  'scripts/publish-nightly.mjs',
  'scripts/publish-nightly.test.mjs',
  'scripts/verify-built-artifact.py',
  'scripts/verify_built_artifact_test.py',
  'scripts/verify-built-artifact.md',
  'scripts/test-nightly.sh',
  'Casks/nocx.rb',
  'Casks/nocx@nightly.rb',
  'scripts/release/promotion.mjs',
  'scripts/release/promotion-github.mjs',
  'scripts/release/promotion.test.mjs',
  'scripts/release/promotion-github.test.mjs',
  'scripts/release/generate-casks.py',
  'scripts/release/generate-casks.test.mjs',
  'scripts/release/README.md',
  'scripts/release/testdata/stable.rb',
  'scripts/release/testdata/nightly-20261026.json',
  'scripts/release/testdata/nightly-20261027.json',
])
export function isProductFile(file) {
  if (pipelineFiles.has(file)) return false
  if (/^(?:\.beads|docs|\.internal|\.githooks\/backlog-gate)\//.test(file)) return false
  if (/^\.githooks\/(?:pre-commit|commit-msg|pre-merge-commit|pre-push)$/.test(file)) return false
  if (file === 'scripts/connect-clone.sh') return false
  if (/^\.githooks\/(?:adr-.*\.mjs|check-adr-identifiers[^/]*)$/.test(file)) return false
  return file.includes('/') || !file.endsWith('.md')
}
export function touchesProduct(files) {
  return files.length === 0 || files.some(isProductFile)
}
