/**
 * NO TEXT TOUCHES THE PANE'S EDGE (ADR-92zzg2y2, the owner's one-gutter rule).
 *
 * Nothing the terminal paints may touch the pane's left, right, top or bottom
 * edge — every side keeps a margin — and the composer's own text starts on the
 * same column a block's header and output start on.
 *
 * ONE GUTTER FOR EVERYTHING: one character cell wide on the left and on the
 * right, half a row on top and on bottom, in every mode — ordinary output,
 * blocks, a plain ssh shell and a full-screen program alike. It is read from
 * the renderer's published cell metric, so it scales with the font. The
 * measurement below checks both halves: the gutter equals the cell (and half
 * the pitch), and the painted text stays inside the box that gutter leaves.
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
  /** The one gutter: one cell sideways, half a row vertically. */
  gutterX: number
  gutterY: number
  cellWidth: number
  cellHeight: number
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
    const pcs = getComputedStyle(pane)
    const inner = pane.querySelector<HTMLElement>('.scrollback-inner')
    const layoutEl = pane.querySelector<HTMLElement>('.scrollback-layout')
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
      gutterX: parseFloat(pcs.getPropertyValue('--pane-inline-padding')) || 0,
      // A custom property holding `calc(...)` is not resolved by
      // `getPropertyValue`; the real padding is.
      gutterY: layoutEl ? parseFloat(getComputedStyle(layoutEl).paddingTop) || 0 : 0,
      cellWidth:
        parseFloat(inner ? getComputedStyle(inner).getPropertyValue('--term-cell-width') : '') || 0,
      cellHeight:
        parseFloat(inner ? getComputedStyle(inner).getPropertyValue('--term-cell-height') : '') ||
        0,
      rows: rows.length,
    }
  })
}

/**
 * Every side clear of the pane's own edge by ONE gutter, and the gutter itself
 * is one cell sideways and half a row vertically (ADR-92zzg2y2). The right and
 * bottom gaps may be larger by the integer-column/row remainder and the
 * scrollbar; the left and top are the declared number.
 */
function assertInside(e: Edges, spoken: string): void {
  expect(e.text, spoken).not.toBeNull()
  expect(e.cellWidth, `${spoken} — a cell width is published`).toBeGreaterThan(0)
  expect(e.cellHeight, `${spoken} — a row pitch is published`).toBeGreaterThan(0)
  expect(
    Math.abs(e.gutterX - e.cellWidth),
    `${spoken} — the gutter is one cell`,
  ).toBeLessThanOrEqual(TOLERANCE)
  expect(
    Math.abs(e.gutterY - e.cellHeight / 2),
    `${spoken} — the gutter is half a row`,
  ).toBeLessThanOrEqual(TOLERANCE)
  const t = e.text!
  expect(t.left - e.pane.left, `${spoken} — left`).toBeGreaterThanOrEqual(e.gutterX - TOLERANCE)
  expect(e.pane.right - t.right, `${spoken} — right`).toBeGreaterThanOrEqual(e.gutterX - TOLERANCE)
  expect(t.top - e.pane.top, `${spoken} — top`).toBeGreaterThanOrEqual(e.gutterY - TOLERANCE)
  expect(e.pane.bottom - t.bottom, `${spoken} — bottom`).toBeGreaterThanOrEqual(
    e.gutterY - TOLERANCE,
  )
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
  // THE COMPOSER'S TEXT IS THE LEDGER'S COLUMN (ADR-92zzg2y2): the block's
  // first painted row and the line a person types start on one x.
  expect(e.composerTextLeft, spoken).not.toBeNull()
  expect(
    Math.abs(e.composerTextLeft! - e.rowLeft!),
    `the composer's text and the block's row start on one column: ${spoken}`,
  ).toBeLessThanOrEqual(TOLERANCE)
  expect(e.rowLeft! - e.pane.left, spoken).toBeGreaterThanOrEqual(e.gutterX - TOLERANCE)

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

test('the gutter is one cell and half a row at two font scales', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)
  await clickIntoEditor(page)
  await page.keyboard.type('printf "SCALE-PROBE\\n"')
  await page.keyboard.press('Enter')
  await expect(
    page.locator('.pane.active .scrollback-inner > .cmd-block .term-grid-row').first(),
  ).toContainText('SCALE-PROBE', { timeout: 30_000 })
  await promptReady(page)

  // THE GUTTER FOLLOWS THE PUBLISHED CELL METRIC (ADR-92zzg2y2). The renderer
  // publishes `--term-cell-width` / `--term-cell-height` on `.pane`; two pairs
  // stand in for two font scales, and both the computed gutter and the painted
  // row's own inset must follow. Read in one evaluate, so a frame cannot
  // republish between the write and the measurement.
  const at = (cellWidth: number, cellHeight: number) =>
    page.evaluate(
      ({ cellWidth, cellHeight }) => {
        const pane = document.querySelector<HTMLElement>('.pane.active')!
        pane.style.setProperty('--term-cell-width', `${cellWidth}px`)
        pane.style.setProperty('--term-cell-height', `${cellHeight}px`)
        const cs = getComputedStyle(pane)
        const layout = pane.querySelector<HTMLElement>('.scrollback-layout')!
        const row = pane.querySelector<HTMLElement>(
          '.scrollback-inner > .cmd-block .term-grid-row',
        )!
        const range = document.createRange()
        range.selectNodeContents(row)
        const p = pane.getBoundingClientRect()
        return {
          gutterX: parseFloat(cs.getPropertyValue('--pane-inline-padding')),
          // The custom property holds `calc(...)`; its real padding resolves.
          gutterY: parseFloat(getComputedStyle(layout).paddingTop),
          rowInset: range.getBoundingClientRect().left - p.left,
        }
      },
      { cellWidth, cellHeight },
    )

  for (const [spoken, cellWidth, cellHeight] of [
    ['small', 8, 16],
    ['large', 13, 26],
  ] as const) {
    const m = await at(cellWidth, cellHeight)
    expect(m.gutterX, `${spoken}: one cell sideways`).toBeCloseTo(cellWidth, 1)
    expect(m.gutterY, `${spoken}: half a row vertically`).toBeCloseTo(cellHeight / 2, 1)
    expect(m.rowInset, `${spoken}: the painted row starts on the gutter`).toBeCloseTo(cellWidth, 1)
  }

  await page.evaluate(() => {
    const pane = document.querySelector<HTMLElement>('.pane.active')!
    pane.style.removeProperty('--term-cell-width')
    pane.style.removeProperty('--term-cell-height')
  })
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
    // THE SHELL MUST HAVE DRAWN BEFORE THE PERSON SPEAKS (nocx-djx1u). A
    // keystroke is an intent, and an intent with no attached session is read
    // and then gone (`sendIntent` in terminal-content.ts refuses it) — the
    // person's line never reaches the pty, so the pane waits for an answer to
    // a question nobody asked. CI run 128820214574, repeat 8 of 10, failed
    // exactly there: this spec typed as soon as the pane was `unstructured`,
    // which is set at attach time, and the shell never echoed the command.
    // What the shell produces is the only honest readiness signal: its first
    // painted row, which exists only once frames are flowing to an attached
    // session.
    await expect
      .poll(
        async () =>
          page.evaluate(() => {
            const rows = Array.from(
              document.querySelectorAll('.pane.active .terminal-live-grid .term-grid-row'),
            )
            return rows.some((row) => (row.textContent ?? '').trim() !== '')
          }),
        { timeout: 30_000 },
      )
      .toBe(true)
    await page.locator('.pane.active .terminal-live-container').click()
    await page.keyboard.type(
      'printf "SSH-MARGIN-PROBE\\n"; for i in $(seq 1 40); do echo "SSH-$i"; done',
    )
    // AND THE SHELL'S ECHO IS THE PROOF THE LINE ARRIVED. Whitespace is
    // stripped because a painted row carries its grid's padding, which a
    // wrapped line would otherwise put in the middle of this text.
    const spoken = (): Promise<string> =>
      page.evaluate(() =>
        (document.querySelector('.pane.active')?.textContent ?? '').replace(/\s+/g, ''),
      )
    await expect
      .poll(async () => (await spoken()).includes('SSH-MARGIN-PROBE'), { timeout: 30_000 })
      .toBe(true)
    await page.keyboard.press('Enter')
    await expect
      .poll(async () => (await spoken()).includes('SSH-40'), { timeout: 30_000 })
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
