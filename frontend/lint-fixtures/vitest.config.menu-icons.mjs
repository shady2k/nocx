import { defineConfig } from 'vitest/config'

/**
 * Vitest config for the menu-icons checker's tests.
 *
 * The repo's vitest.config.ts restricts `include` to src test files only,
 * which is right for application tests but excludes checker tests that live
 * beside their scripts. This config gives vitest exactly the one file:
 *
 *   ./node_modules/.bin/vitest run --config lint-fixtures/vitest.config.menu-icons.mjs
 *
 * Run from frontend/. No solid plugin needed: the scanner is pure node.
 */
export default defineConfig({
  test: {
    environment: 'node',
    include: ['lint-fixtures/check-menu-icons.test.mjs'],
  },
})
