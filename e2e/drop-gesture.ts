/**
 * The gesture itself: what a browser does when a person drops a file onto a
 * pane, performed from the outside.
 *
 * Factored out of e2e/upload.spec.ts when the local branch got its own spec
 * (e2e/local-drop.spec.ts, nocx-9le.5.23). The two specs assert entirely
 * different things — one asks a remote host what it holds, the other reads
 * the harness's own disk — but the gesture between them is one behaviour, and
 * a second copy of it is a second owner of "what does a browser drop look
 * like": they would agree until the day the drop module changed and only one
 * was updated.
 *
 * What deliberately stays in the specs is everything either one could get
 * wrong on its own — where the tab is, what the destination is, and what
 * proves the bytes arrived. This module knows only how to dispatch a drag.
 */
import { expect, type Page } from '@playwright/test'

/** The pane element is the drop target: TerminalContent marks it when the
 *  session opens and clears it when the session dies, so the selector also
 *  asserts there IS a session behind the tab. */
export const DROP_TARGET = '.pane.active[data-file-drop-target]'

/**
 * Drag `payload` over the active pane and drop it there, as a browser would.
 *
 * The dragover is dispatched first and carries its own assertion:
 * `data-drop-active` is the drop module's statement that it recognised a
 * files drag and took the event. Without it, a drop that does nothing is
 * indistinguishable from a drop the handler never saw.
 *
 * The DataTransfer is parked on `window` between the two evaluates because a
 * second page.evaluate cannot be handed one built in the first.
 */
export async function dropFileOnActivePane(
  page: Page,
  fileName: string,
  payload: Buffer,
): Promise<void> {
  const target = page.locator(DROP_TARGET)
  await expect(target).toHaveCount(1)

  await page.evaluate(
    ({ selector, name, b64 }) => {
      const el = document.querySelector(selector)
      if (!(el instanceof HTMLElement)) throw new Error(`no drop target for ${selector}`)
      const binary = atob(b64)
      const bytes = new Uint8Array(binary.length)
      for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
      const dt = new DataTransfer()
      dt.items.add(new File([bytes], name, { type: 'application/octet-stream' }))
      ;(window as unknown as { __nocxDrop?: DataTransfer }).__nocxDrop = dt
      el.dispatchEvent(
        new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true }),
      )
    },
    { selector: DROP_TARGET, name: fileName, b64: payload.toString('base64') },
  )
  await expect(target).toHaveAttribute('data-drop-active', '')

  await page.evaluate((selector) => {
    const el = document.querySelector(selector)
    if (!(el instanceof HTMLElement)) throw new Error(`no drop target for ${selector}`)
    const dt = (window as unknown as { __nocxDrop?: DataTransfer }).__nocxDrop
    if (dt === undefined) throw new Error('the drag transfer did not survive to the drop')
    el.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }))
  }, DROP_TARGET)
}
