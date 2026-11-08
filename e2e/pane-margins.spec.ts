/**
 * NO TEXT TOUCHES THE PANE'S EDGE (ADR-2yzsq8mk, the owner's edge pass).
 *
 * The owner, looking at a normal command and at a plain ssh shell: nothing the
 * terminal paints may touch the pane's left, right, top or bottom edge — every
 * side keeps a margin — and the composer's own text starts on the same column
 * a block's header and output start on, about 24px from the pane.
 *
 * One geometry, four sides, and the same two places in every mode that is not
 * the alternate screen: the ledger's gutter inline (`--pane-inline-padding`,
 * paid by the rows) and the same token as the vertical margin of the live box
 * (`runningLiveCap`, `paneFillHeight`). A full-screen program is the exception
 * the decision names: flush left, the whole width, one row of bottom margin.
 *
 * The measurement is the UNION of every painted row, intersected with the
 * pane's own box, so a row the scroller clips contributes only what a person
 * can see. A wait is always on a settled state, never on a duration.
 */
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { test, expect, clickIntoEditor, promptReady, resolveBackend } from './harness'
import type { Page } from '@playwright/test'
import { startSshd, rpc } from './sshd-fixture'
import { readStand } from './stand'

/** The ledger's inset, the token the stylesheet and the fit both read. */
const MARGIN = 24
/** Sub-pixel rounding and the remainder of an integer column count. */
const TOLERANCE = 1.5

interface Edges {
  layout: { top: number; bottom: number; padTop: string; padBottom: string; cls: string }
  scroller: { top: number; bottom: number }
  pane: { left: number; right: number; top: number; bottom: number }
  /** The painted text's box, clipped to what the pane actually shows. */
  text: { left: number; right: number; top: number; bottom: number } | null
  /** The first painted row's left edge, and the composer's first line's. */
  rowLeft: number | null
  composerTextLeft: number | null
  rows: number
}

async function edges(page: Page): Promise<Edges> {
  return page.evaluate(() => {
    const r2 = (v: number): number => Math.round(v * 100) / 100
    const pane = document.querySelector<HTMLElement>('.pane.active')
    if (!pane) throw new Error('no active pane')
    const pr = pane.getBoundingClientRect()
    // WHAT A PERSON CAN SEE: a row is clipped by the scroller it scrolls in and,
    // in the live region, by the box that holds it — an idle live region is
    // zero pixels tall and still has rows with geometry.
    const scroller = pane.querySelector<HTMLElement>('.scrollback-area')
    const sr = scroller ? scroller.getBoundingClientRect() : pr
    const clipOf = (row: HTMLElement): DOMRect => {
      const box = row.closest<HTMLElement>('.terminal-live-container')
      return box ? box.getBoundingClientRect() : sr
    }
    const rows = [...pane.querySelectorAll<HTMLElement>('.term-grid-row')].filter(
      (row) => row.getBoundingClientRect().height > 0,
    )
    let box: { left: number; right: number; top: number; bottom: number } | null = null
    for (const row of rows) {
      const b = row.getBoundingClientRect()
      const clip = clipOf(row)
      const left = Math.max(b.left, pr.left, sr.left, clip.left)
      const right = Math.min(b.right, pr.right, sr.right, clip.right)
      const top = Math.max(b.top, pr.top, sr.top, clip.top)
      const bottom = Math.min(b.bottom, pr.bottom, sr.bottom, clip.bottom)
      if (right <= left || bottom <= top) continue
      box = box ?? { left, right, top, bottom }
      box.left = Math.min(box.left, left)
      box.right = Math.max(box.right, right)
      box.top = Math.min(box.top, top)
      box.bottom = Math.max(box.bottom, bottom)
    }
    const first = rows[0] ?? null
    const line = pane.querySelector<HTMLElement>('.cm-line')
    const layout = pane.querySelector<HTMLElement>('.scrollback-layout')
    const lr = layout ? layout.getBoundingClientRect() : pr
    const lcs = layout ? getComputedStyle(layout) : null
    const csr = scroller ? scroller.getBoundingClientRect() : pr
    return {
      layout: {
        top: r2(lr.top),
        bottom: r2(lr.bottom),
        padTop: lcs?.paddingTop ?? '',
        padBottom: lcs?.paddingBottom ?? '',
        cls: layout?.className ?? '',
      },
      scroller: { top: r2(csr.top), bottom: r2(csr.bottom) },
      pane: { left: r2(pr.left), right: r2(pr.right), top: r2(pr.top), bottom: r2(pr.bottom) },
      text: box
        ? {
            left: r2(box.left),
            right: r2(box.right),
            top: r2(box.top),
            bottom: r2(box.bottom),
          }
        : null,
      rowLeft: first ? r2(first.getBoundingClientRect().left) : null,
      composerTextLeft: line ? r2(line.getBoundingClientRect().left) : null,
      rows: rows.length,
    }
  })
}

/** Every side clear of the pane's own edge by the ledger's margin. */
function assertInside(e: Edges, spoken: string): void {
  expect(e.text, spoken).not.toBeNull()
  const t = e.text!
  expect(t.left - e.pane.left, `${spoken} — left`).toBeGreaterThanOrEqual(MARGIN - TOLERANCE)
  expect(e.pane.right - t.right, `${spoken} — right`).toBeGreaterThanOrEqual(MARGIN - TOLERANCE)
  expect(t.top - e.pane.top, `${spoken} — top`).toBeGreaterThanOrEqual(MARGIN - TOLERANCE)
  expect(e.pane.bottom - t.bottom, `${spoken} — bottom`).toBeGreaterThanOrEqual(MARGIN - TOLERANCE)
}

test('a normal command keeps every edge, and its composer starts on the same column', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)
  await clickIntoEditor(page)
  await page.keyboard.type('printf "MARGIN-PROBE\\n"')
  await page.keyboard.press('Enter')
  await expect(
    page.locator('.pane.active .scrollback-inner > .cmd-block .term-grid-row').first(),
  ).toContainText('MARGIN-PROBE', { timeout: 30_000 })
  await promptReady(page)
  // The frozen card, the prompt and its composer, all settled.
  await expect.poll(async () => (await edges(page)).text !== null, { timeout: 10_000 }).toBe(true)
  const e = await edges(page)
  const spoken = `frozen command: ${JSON.stringify(e)}`
  assertInside(e, spoken)
  // THE COMPOSER'S TEXT IS THE LEDGER'S COLUMN (ADR-2yzsq8mk): the block's
  // first painted row and the line a person types start on one x.
  expect(e.composerTextLeft, spoken).not.toBeNull()
  expect(
    Math.abs(e.composerTextLeft! - e.rowLeft!),
    `the composer's text and the block's row start on one column: ${spoken}`,
  ).toBeLessThanOrEqual(TOLERANCE)
  expect(e.rowLeft! - e.pane.left, spoken).toBeGreaterThanOrEqual(MARGIN - TOLERANCE)

  // AND WHILE IT RUNS: the composer is gone, so the live box's own bottom
  // margin is what keeps the last row off the pane's edge.
  await clickIntoEditor(page)
  await page.keyboard.type('for i in $(seq 1 40); do echo "RUN-$i"; done; sleep 300')
  await page.keyboard.press('Enter')
  await expect
    .poll(
      async () =>
        page.evaluate(() =>
          document
            .querySelector('.pane.active .terminal-live-grid')
            ?.textContent?.includes('RUN-20'),
        ),
      { timeout: 30_000 },
    )
    .toBe(true)
  const running = await edges(page)
  assertInside(running, `running command: ${JSON.stringify(running)}`)
  await page.keyboard.press('Control+C')
})

test('a plain ssh shell keeps every edge too', async ({ page }) => {
  test.setTimeout(180_000)
  const remoteHome = mkdtempSync(path.join(tmpdir(), 'margin-remote-'))
  const remoteCwd = mkdtempSync(path.join(tmpdir(), 'margin-cwd-'))
  const fixture = await startSshd({ home: remoteHome, cwd: remoteCwd })
  let createdId: string | null = null
  try {
    const sshDir = path.join(readStand().home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), fixture.knownHosts + '\n')
    await page.goto('/')
    await promptReady(page)
    const wsInfo = await resolveBackend(page)
    const profileName = `pane-margins-${Date.now()}`
    const created = await rpc<{ id: string }>(page, wsInfo, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: fixture.user,
        keyPath: fixture.userKey,
        // No integration is requested at all: the session is conventional by
        // design, which is the markerless mode this case is about.
        desiredMode: 'raw',
      },
    })
    createdId = created.id
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible()
    await search.fill(profileName)
    await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
      timeout: 10_000,
    })
    await page.keyboard.press('Enter')
    // A MARKERLESS SESSION: no integration is requested, so the terminal itself
    // fills the pane and there is no composer at all — the person types at the
    // shell. That is the mode this case is about; a composer submit would open
    // an app-owned block and make the session structured, which case 1 covers.
    await expect
      .poll(
        async () =>
          page.evaluate(
            () =>
              document
                .querySelector('.pane.active .terminal-live-container')
                ?.classList.contains('live-unstructured') ?? false,
          ),
        { timeout: 30_000 },
      )
      .toBe(true)
    await page.locator('.pane.active .terminal-live-container').click()
    await page.keyboard.type(
      'printf "SSH-MARGIN-PROBE\\n"; for i in $(seq 1 40); do echo "SSH-$i"; done',
    )
    await page.keyboard.press('Enter')
    await expect
      .poll(
        async () =>
          page.evaluate(() =>
            (document.querySelector('.pane.active')?.textContent ?? '').includes('SSH-40'),
          ),
        { timeout: 30_000 },
      )
      .toBe(true)
    await expect.poll(async () => (await edges(page)).rows, { timeout: 10_000 }).toBeGreaterThan(5)
    const e = await edges(page)
    assertInside(e, `plain ssh shell: ${JSON.stringify(e)}`)
  } finally {
    try {
      const info = await resolveBackend(page)
      if (createdId !== null) await rpc(page, info, 'profiles.delete', { id: createdId })
    } catch {
      /* the pane may already be gone */
    }
    fixture.proc.kill('SIGKILL')
  }
})
