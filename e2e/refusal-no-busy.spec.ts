// A failing filesystem call names itself in the panel — and nothing in the
// product says "busy".
//
// ADR-8eej3e66 stage 1 removed one sentence from the product: "The terminal
// is busy — that action was refused. Try again in a moment." Stage 8 removed
// the thing underneath it: there is no capacity refusal any more (no -32004,
// no control.saturated notification, no lane), so the class of failure this
// spec used to drive no longer exists. What remains true, and is what this
// spec now proves, is the half stage 1 built: a filesystem call that FAILS is
// answered where the person asked, by the panel's own state with its own
// Retry, and never by a sentence about the terminal being busy.
//
// The fault is injected at the ONE seam every control frame already passes
// (the page's WebSocket), the way restore-uncertain-adoption.spec.ts injects
// its own: the backend's ordinary internal-error answer (-32603, the shape a
// provider failure produces) is returned for every `files.*` frame while the
// flag is set. Everything else — the session, the panes, the stand — is real.
import { test, expect, appReadyForInput } from './harness'
import type { Page } from '@playwright/test'

/** Fail every `files.*` request with the backend's ordinary internal-error
 *  answer while `window.__nocxFailFiles` is true. */
async function installFilesFailure(page: Page, failing: boolean): Promise<void> {
  await page.addInitScript((startFailing: boolean) => {
    type FaultWindow = { __nocxFailFiles?: boolean }
    const fault = window as unknown as FaultWindow
    fault.__nocxFailFiles = startFailing
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
          fault.__nocxFailFiles !== true ||
          message.method === undefined ||
          !message.method.startsWith('files.')
        ) {
          super.send(data)
          return
        }
        // The backend's ordinary failure answer (-32603, internal error), which
        // is what a provider that cannot read a directory produces. The code is
        // deliberately NOT -32004: that refusal no longer exists, and a spec
        // that injected it would be testing a shape the server cannot send.
        const error = { code: -32603, message: 'the folder could not be read' }
        queueMicrotask(() => {
          this.dispatchEvent(
            new MessageEvent('message', {
              data: JSON.stringify({ jsonrpc: '2.0', id: message.id, error }),
            }),
          )
        })
      }
    }
  }, failing)
}

/**
 * The removed sentence — and the phrases it was built on — asserted against
 * what is actually on screen, not against a component's state.
 *
 * The bare word "busy" is deliberately NOT asserted: this suite runs in a
 * worktree whose branch name may contain it (`…-no-busy-toast`), and the
 * prompt line legitimately prints the branch. A test that matches the word
 * anywhere measures the branch it happens to be running on, not the product.
 * These three phrases cannot appear for any reason but the one being removed.
 */
async function expectNoBusySentence(page: Page): Promise<void> {
  await expect(page.getByText(/the terminal is busy/i)).toHaveCount(0)
  await expect(page.getByText(/control plane busy/i)).toHaveCount(0)
  await expect(page.getByText(/try again in a moment/i)).toHaveCount(0)
  await expect(page.locator('.ui-toast__message')).toHaveCount(0)
}

test('a failing filesystem call names itself in the panel and never says "busy"', async ({
  page,
}) => {
  await installFilesFailure(page, true)
  await page.goto('/')
  await appReadyForInput(page)

  // The panel is open on Files from cold start, and its open failed. Its OWN
  // state answers: what failed, where it was asked for, and Retry.
  const failure = page.locator('[data-testid="files-error"]')
  await expect(failure).toBeVisible({ timeout: 20_000 })
  await expect(failure).toContainText(/could not be read/i)
  await expect(failure).not.toContainText(/busy/i)
  await expect(page.locator('[data-testid="files-retry-open"]')).toBeVisible()
  await expectNoBusySentence(page)

  // The backend answers again. The person presses the retry the panel offered
  // — but the panel may get there first, and that is its own rule working: a
  // failed open is re-asked by the next natural trigger (an origin change
  // re-scopes it), and the shell reports its cwd more than once. So the click
  // is best-effort and the OUTCOME is what is asserted: whichever ask lands
  // first, the tree opens and nothing says the terminal is busy.
  await page.evaluate(() => {
    ;(window as unknown as { __nocxFailFiles?: boolean }).__nocxFailFiles = false
  })
  const retry = page.locator('[data-testid="files-retry-open"]')
  if ((await retry.count()) > 0) {
    await retry.click({ timeout: 2_000 }).catch(() => {
      // Detached between the count and the click: the panel re-asked by
      // itself and the failure state is already gone. Same outcome, other
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

// AND THE REMOVED REFUSAL IS NOT ON THE WIRE (ADR-8eej3e66 stage 8). A person's
// call that is waited on is answered with the declaration's own outcome, never
// with -32004: the code does not exist and no surface handles it any more.
test('no control frame is answered with the removed capacity refusal', async ({ page }) => {
  const codes: number[] = []
  page.on('websocket', (socket) => {
    socket.on('framereceived', (frame) => {
      if (typeof frame.payload !== 'string') return
      const match = /"code":\s*(-?\d+)/.exec(frame.payload)
      if (match !== null) codes.push(Number(match[1]))
    })
  })
  await page.goto('/')
  await appReadyForInput(page)
  // Something that can move a row: the Files panel asks for an open and a
  // listing on the tab in front, and every answer lands in the frames above.
  await page.locator('.ui-tree-row').first().waitFor({ timeout: 20_000 })
  expect(codes).not.toContain(-32004)
})
