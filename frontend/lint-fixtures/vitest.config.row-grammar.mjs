import { defineConfig } from 'vitest/config'

/**
 * Vitest config for the row-grammar checker's tests.
 *
 * The repo's vitest.config.ts restricts `include` to src test files only,
 * which is right for application tests but excludes checker tests that live
 * beside their scripts. This config gives vitest exactly the one file:
 *
 *   ./node_modules/.bin/vitest run --config lint-fixtures/vitest.config.row-grammar.mjs
 *
 * Run from frontend/. No solid plugin needed: the scanner is pure node.
 */
export default defineConfig({
  test: {
    environment: 'node',
    include: ['lint-fixtures/check-row-grammar.test.mjs'],
  },
})
