import { defineConfig } from 'vitest/config'

// The node environment is deliberate: the modules under test (frame.ts, ipc.ts)
// touch no DOM, and the WebSocket they do touch is stubbed per test. Renderer
// tests, when they arrive, will need jsdom — add it then, per-file via the
// `// @vitest-environment jsdom` pragma, rather than paying for it everywhere.
// Tab bar tests (tabs.test.ts) use jsdom for DOM assertions.
export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
