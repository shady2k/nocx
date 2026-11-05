// A refused request never says "busy" — end to end, at the real backend.
//
// ADR-8eej3e66 stage 1 removes one sentence from the product: "The terminal
// is busy — that action was refused. Try again in a moment." It fixes nothing
// underneath, so refusals still arrive — and this spec drives one at the
// place a person meets it, with the real backend, the real renderer and the
// real wire.
//
// The fault is injected at the ONE seam every control frame already passes
// (the page's WebSocket), the way restore-uncertain-adoption.spec.ts injects
// its own: the server's own refusal envelope (-32004, reason
// control-saturated, scope filesystem, retryable) is answered for every
// `files.*` frame while the flag is set. Everything else — the session, the
// panes, the stand — is real.
//
// What a person does: opens nocx with the Files panel on screen while the
// filesystem admission refuses work. What they must see: the panel's own
// failure, named where it happened, with Retry — and no sentence anywhere on
// the screen that calls the terminal busy or asks them to try again in a
// moment. Then, once the admission accepts work again, the same Retry opens
// the tree.
import { test, expect, appReadyForInput } from './harness'
import type { Page } from '@playwright/test'

/** Answer every `files.*` request with the server's saturation refusal while
 *  `window.__nocxRefuseFiles` is true. */
async function installFilesRefusal(page: Page, refusing: boolean): Promise<void> {
  await page.addInitScript((startRefusing: boolean) => {
    type FaultWindow = { __nocxRefuseFiles?: boolean }
    const fault = window as unknown as FaultWindow
    fault.__nocxRefuseFiles = startRefusing
    const Native = window.WebSocket
    window.WebSocket = class extends Native {
      override send(data: Parameters<WebSocket['send']>[0]): void {
        if (typeof data !== 'string') {
          super.send(data)
          return
        }
        let message: { id?: number; method?: string }
        try {
          message = JSON.parse(data) as { id?: number; method?: string }
        } catch {
          super.send(data)
          return
        }
        if (
          fault.__nocxRefuseFiles !== true ||
          message.method === undefined ||
          !message.method.startsWith('files.')
        ) {
          super.send(data)
          return
        }
        // The server's own envelope, verbatim in shape (internal/transport/
        // ws_saturation.go): the fixed reason, the admission's name, retryable.
        const error = {
          code: -32004,
          message: 'Control plane busy',
          data: {
            reason: 'control-saturated',
            scope: 'filesystem',
            retryable: true,
            retryAfterMs: 50,
          },
        }
        queueMicrotask(() => {
          this.dispatchEvent(
            new MessageEvent('message', {
              data: JSON.stringify({ jsonrpc: '2.0', id: message.id, error }),
            }),
          )
        })
      }
    }
  }, refusing)
}

/**
 * The removed sentence — and the phrases it was built on — asserted against
 * what is actually on screen, not against a component's state.
 *
 * The bare word "busy" is deliberately NOT asserted: this suite runs in a
 * worktree whose branch name contains it (`…-no-busy-toast`), and the prompt
 * line legitimately prints the branch. A test that matches the word anywhere
 * measures the branch it happens to be running on, not the product. These
 * three phrases cannot appear for any reason but the one being removed.
 */
async function expectNoBusySentence(page: Page): Promise<void> {
  await expect(page.getByText(/the terminal is busy/i)).toHaveCount(0)
  await expect(page.getByText(/control plane busy/i)).toHaveCount(0)
  await expect(page.getByText(/try again in a moment/i)).toHaveCount(0)
  await expect(page.locator('.ui-toast__message')).toHaveCount(0)
}

test('a refused filesystem call names itself in the panel and never says "busy"', async ({
  page,
}) => {
  await installFilesRefusal(page, true)
  await page.goto('/')
  await appReadyForInput(page)

  // The panel is open on Files from cold start, and its open was refused.
  // Its OWN state answers: what failed, where it was asked for, and Retry.
  const failure = page.locator('[data-testid="files-error"]')
  await expect(failure).toBeVisible({ timeout: 20_000 })
  await expect(failure).toContainText(/refused/i)
  await expect(failure).not.toContainText(/busy/i)
  await expect(page.locator('[data-testid="files-retry-open"]')).toBeVisible()
  await expectNoBusySentence(page)

  // The admission accepts work again. The person presses the retry the panel
  // offered — but the panel may get there first, and that is its own rule
  // working: a failed open is re-asked by the next natural trigger (an origin
  // change re-scopes it), and the shell reports its cwd more than once. So the
  // click is best-effort and the OUTCOME is what is asserted: whichever ask
  // lands first, the tree opens and nothing says the terminal is busy.
  await page.evaluate(() => {
    ;(window as unknown as { __nocxRefuseFiles?: boolean }).__nocxRefuseFiles = false
  })
  const retry = page.locator('[data-testid="files-retry-open"]')
  if ((await retry.count()) > 0) {
    await retry.click({ timeout: 2_000 }).catch(() => {
      // Detached between the count and the click: the panel re-asked by
      // itself and the refusal state is already gone. Same outcome, other
      // trigger — asserted below.
    })
  }
  await expect(page.locator('[data-testid="files-error"]')).toHaveCount(0, { timeout: 20_000 })
  await expect(page.locator('[data-testid="files-panel"]')).toHaveAttribute('data-root', '/', {
    timeout: 20_000,
  })
  await expect(page.locator('.ui-tree-row').first()).toBeVisible({ timeout: 20_000 })
  await expectNoBusySentence(page)
})
