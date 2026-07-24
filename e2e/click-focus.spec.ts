import { test, expect, type Page } from "./harness";

// Regression guard for the shared half of nocx-d1f: with one tab, clicking the
// window left the terminal unable to take input.
//
// Scope, stated honestly. Two fixes closed that bug:
//   - 2c02a46 stopped --wails-draggable leaking onto the pane, so Wails no
//     longer swallowed the mousedown;
//   - 25de485 made the ResizeObserver repaint after clearing the texture atlas.
// Neither is provable from here. --wails-draggable is an inert custom property
// in Chromium — only the native WKWebView reads it — and an e2e attempt at the
// atlas half passed just as happily with the fix reverted, so it was deleted
// rather than kept as a guard that guards nothing (see nocx-bq7).
//
// What this file does prove is that the path they share — click, focus,
// keystroke, PTY, response — is unbroken. That is worth locking on its own: it
// is the path every one of those bugs travelled through.
//
// Post nocx-4ff the editor owns input at every prompt (ADR-0004). The focus
// target is therefore .nocx-editor-input (the CommandEditor textarea), not
// .xterm-helper-textarea (the raw terminal grid). The path itself is identical.

const PANE = ".pane.active";
const TITLE = ".tab-title";

test("a click into the pane leaves the terminal taking keystrokes", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".tab")).toHaveCount(1);
  await page.waitForTimeout(1500); // shell start + first paint

  // Move focus off the editor first. Without this the assertion is vacuous:
  // the tab is focused on load, so a click that changed nothing would pass.
  await page.locator(".tabbar-spacer").click();
  await expect
    .poll(() => page.evaluate(() => document.activeElement?.className ?? ""))
    .not.toContain("nocx-editor-input");

  const box = await page.locator(PANE).boundingBox();
  await page.mouse.click(box!.x + box!.width / 2, box!.y + box!.height / 2);

  await expect
    .poll(() => page.evaluate(() => document.activeElement?.className ?? ""))
    .toContain("nocx-editor-input");

  // The tab title is the only DOM-observable end of the keystroke round trip:
  // once WebGL paints to a canvas, the screen text is not in the DOM at all.
  // An OSC 0 sequence is shell-agnostic, so this holds on a runner's bash just
  // as it does on the developer's zsh.
  await page.keyboard.type("printf '\\033]0;NOCX-D1F-CLICK\\007'\n");
  await expect(page.locator(TITLE).first()).toHaveText("NOCX-D1F-CLICK");
});
