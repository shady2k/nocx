import { defineConfig } from 'vitest/config'
import solid from 'vite-plugin-solid'

// Default environment is node — chosen deliberately so tests that touch no DOM
// (539 of them) do not pay for one. Solid component tests use the
// // @vitest-environment jsdom pragma per-file.
// Solid component tests also need the development/browser export condition.
// Without resolve.conditions, vitest resolves the server build of solid-js
// and reactivity misbehaves with no useful error. Existing non-Solid tests
// that use the // @vitest-environment jsdom pragma keep working unchanged.
// The solid plugin is registered so .tsx files import solid-js correctly.
export default defineConfig({
  plugins: [solid()],
  resolve: {
    conditions: ['development', 'browser'],
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
    // Generated contract DTOs (src/generated/*.ts) are never tests, but a
    // method whose last segment is a vitest keyword — endpoints.test,
    // sessions.spec — would emit a file matching the include globs above.
    // The method names are wire vocabulary; the exclude is what keeps a
    // future `.test`/`.spec`/`.bench` method from breaking the suite.
    exclude: ['src/generated/**'],
    setupFiles: ['./vitest.setup.ts'],
    // A BUDGET, NOT AN EXPECTATION ABOUT SPEED. Vitest's own default is five
    // seconds, and nothing here ever chose it: that is fine for the thousands
    // of tests that finish in milliseconds and far too tight for the handful
    // that MOUNT THE WHOLE APP and then walk it. Those take one to three
    // seconds on an idle machine, so five leaves no headroom at all — they go
    // green alone and red under a loaded parallel run, which is the flake
    // shape AGENTS.md names and refuses.
    //
    // MEASURED 2026-08-26 (nocx-jzlsf): with the suite at 4957 tests across
    // 270 files, one case of 'secrets are doors in every request field' timed
    // out at 5000ms in a pre-commit run and passed in 692ms when its file ran
    // alone. Nothing about that test had changed; the suite had merely grown
    // enough to run it under contention.
    //
    // Thirty seconds is a HANG DETECTOR. Every wait in this suite is on an
    // observable state — a rendered node, a resolved call — so a test that
    // reaches this number is stuck, not slow, and no test is kept alive by
    // it that a faster machine would have caught.
    testTimeout: 30_000,
    // The same reasoning for beforeEach/beforeAll: a hook that mounts the app
    // is doing the same work under the same contention.
    hookTimeout: 30_000,
  },
})
