import { defineConfig } from 'vitest/config'

/**
 * Vitest config for the lint-fixtures checker tests.
 *
 * The repo's vitest.config.ts restricts `include` to src test files only,
 * which is right for application tests but excludes checker tests that live
 * beside their scripts. This config runs every checker test in the
 * directory — one config, not one per rule:
 *
 *   ./node_modules/.bin/vitest run --config lint-fixtures/vitest.config.lint-fixtures.mjs
 *
 * Run from frontend/, through `npm test` (the second half of the chain), so
 * the containerized pre-commit runner picks it up automatically. No solid
 * plugin needed: every checker scanner is pure node.
 */
export default defineConfig({
  test: {
    environment: 'node',
    include: ['lint-fixtures/**/*.test.mjs'],
  },
})
