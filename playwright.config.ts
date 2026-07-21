import { defineConfig } from '@playwright/test'

// e2e drives the whole app, not the frontend alone: `wails dev` serves the
// built UI *and* the bound Go methods at :34115, so a test here exercises the
// real transport, the real PTY and the real renderer. That is the only place
// layout, focus and GPU behaviour are observable — jsdom has none of them.
export default defineConfig({
  testDir: './e2e',
  timeout: 60_000,
  use: {
    baseURL: 'http://localhost:34115',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
})
