import { test as base } from "@playwright/test";

export { expect } from "@playwright/test";
export type { Page } from "@playwright/test";

// Shared e2e harness. When the suite runs against the headless
// vite + devharness shim (NOCX_WS_PORT set) instead of `wails dev`, inject the
// Wails GetWSPort binding the frontend expects before any app code runs. Under
// `wails dev` the real binding is present and NOCX_WS_PORT is unset, so this is
// a no-op — the same specs run unchanged in CI.
export const test = base.extend({
  page: async ({ page }, use) => {
    const port = process.env.NOCX_WS_PORT;
    if (port) {
      await page.addInitScript((p) => {
        (window as unknown as { go: unknown }).go = {
          main: {
            WailsApp: {
              GetWSPort: () => Promise.resolve(Number(p)),
              CheckForUpdate: () => Promise.resolve(null),
              ReportHealthy: () => Promise.resolve(),
              ApplyUpdate: () => Promise.resolve(),
            },
          },
        };
      }, port);
    }
    await use(page);
  },
});
