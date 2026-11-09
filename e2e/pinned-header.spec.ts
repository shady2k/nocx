/**
 * A BLOCK'S HEADER STAYS AT THE TOP OF THE PANE (nocx-4y82c.1.3).
 *
 * The owner, from Warp: when a block's output is taller than the pane, its
 * header — the directory and time, the status, the command and its actions —
 * stays pinned at the top while the output scrolls under it, so a person
 * always sees which command they are reading and can stop it. One pinned
 * header at a time: the block the top of the view is in. Running or finished
 * alike. It is the block's OWN header, the same CommandBlockFrame a
 * full-screen program keeps above its grid (ADR-3kqrem7v), not a second copy
 * with its own look.
 *
 * WHAT IS MEASURED, and why each one is a person's complaint:
 *
 *  1. PINNED AND RIGHT. Scroll into the middle of a tall frozen block's
 *     output: its header's top edge is the scroller's top edge, the command it
 *     shows is that block's, and its actions work from there (the ⋮ menu
 *     opens, Stop stops a running command).
 *  2. NO ROW MOVED. The header must not move a single row (ADR-2yzsq8mk,
 *     PR #752, merged the day this was asked for). Every output row keeps its
 *     position in the scroller's CONTENT between the unpinned frame and the
 *     pinned one, and the block keeps its own offset and height. A sticky
 *     header that took layout space would show up here as a row that moved by
 *     the header's height.
 *  3. ONE, AND THE RIGHT ONE. Scrolling on to the next tall block swaps the
 *     pinned header for that block's. Exactly one header sits at the top, at
 *     every position, and it is always the header of the block the top of the
 *     view is in.
 *
 * The rows are read from the frozen body's own `.term-grid-row`s, which are
 * painted asynchronously after the command freezes, so every measurement
 * waits for the marker line the command ended with rather than for a
 * duration.
 */
import type { Page } from '@playwright/test'
import { test, expect, promptReady, clickIntoEditor } from './harness'

/** Sub-pixel layout and a fractional scrollTop. */
const TOLERANCE = 1

const SHORT = 'printf "SHORT-BLOCK\\n"'
const LONG_A = 'printf "A-START\\n"; for i in $(seq 1 140); do echo "A-ROW-$i"; done; echo "A-END"'
const LONG_B = 'printf "B-START\\n"; for i in $(seq 1 140); do echo "B-ROW-$i"; done; echo "B-END"'
/** The running command: a long burst, then a sleep long enough to scroll in. */
const RUNNING = 'printf "RUN-START\\n"; for i in $(seq 1 160); do echo "RUN-$i"; done; sleep 120'

interface HeaderReading {
  readonly command: string
  /** The header's top edge, relative to the scroller's top edge. */
  readonly top: number
  /** The header's own height: a long command wraps its header this tall. */
  readonly height: number
  /** Whether the controller says this header is the one held at the top. */
  readonly stuck: boolean
  /** Whether the controller allows this header to pin at all. */
  readonly pinnable: boolean
  /** Whether this is the one-line running variance (ADR-3kqrem7v). */
  readonly running: boolean
}

interface RowReading {
  readonly text: string
  /** The row's top edge in the scroller's CONTENT space. */
  readonly y: number
}

interface BlockReading {
  readonly command: string
  readonly running: boolean
  readonly header: HeaderReading | null
  /** The block's own top in the scroller's CONTENT space. */
  readonly top: number
  readonly height: number
  readonly rows: readonly RowReading[]
}

interface Reading {
  readonly scrollTop: number
  readonly scrollHeight: number
  readonly clientHeight: number
  /** Every header whose top edge IS the scroller's top edge. */
  readonly pinned: readonly HeaderReading[]
  readonly blocks: readonly BlockReading[]
}

async function read(page: Page): Promise<Reading> {
  return page.evaluate(() => {
    const round = (value: number): number => Math.round(value * 100) / 100
    const pane = document.querySelector<HTMLElement>('.pane.active')
    if (!pane) throw new Error('no active pane')
    const scroller = pane.querySelector<HTMLElement>('.scrollback-area')
    if (!scroller) throw new Error('no scrollback scroller')
    const sr = scroller.getBoundingClientRect()
    const blocks = [...pane.querySelectorAll<HTMLElement>('.scrollback-inner > .cmd-block')].map(
      (block) => {
        const br = block.getBoundingClientRect()
        const header = block.querySelector<HTMLElement>(':scope > .cmd-header')
        const text = header?.querySelector<HTMLElement>('.cmd-header-text')
        const hr = header?.getBoundingClientRect() ?? null
        return {
          command: (text?.textContent ?? '').trim(),
          running: block.classList.contains('cmd-block-running'),
          header:
            header && hr
              ? {
                  command: (text?.textContent ?? '').trim(),
                  top: round(hr.top - sr.top),
                  height: round(hr.height),
                  stuck: header.hasAttribute('data-stuck'),
                  pinnable: header.getAttribute('data-pinnable') !== 'false',
                  running: header.dataset.variant === 'running',
                }
              : null,
          top: round(br.top - sr.top + scroller.scrollTop),
          height: round(br.height),
          rows: [...block.querySelectorAll<HTMLElement>('.cmd-output .term-grid-row')].map(
            (row) => ({
              text: (row.textContent ?? '').trimEnd(),
              y: round(row.getBoundingClientRect().top - sr.top + scroller.scrollTop),
            }),
          ),
        }
      },
    )
    return {
      scrollTop: round(scroller.scrollTop),
      scrollHeight: round(scroller.scrollHeight),
      clientHeight: round(scroller.clientHeight),
      pinned: blocks
        .map((block) => block.header)
        .filter((header): header is HeaderReading => header !== null && Math.abs(header.top) < 0.5),
      blocks,
    }
  })
}

async function scrollTo(page: Page, top: number): Promise<void> {
  await page.evaluate((value) => {
    const scroller = document.querySelector<HTMLElement>('.pane.active .scrollback-area')
    if (!scroller) throw new Error('no scrollback scroller')
    scroller.scrollTop = value
  }, top)
}

async function runCommand(page: Page, command: string, marker: string): Promise<void> {
  await clickIntoEditor(page)
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  // The marker is the command's own last line: the block has frozen and its
  // rows are painted only when the body holds it.
  await expect
    .poll(
      async () =>
        page.evaluate(
          (text) =>
            [...document.querySelectorAll<HTMLElement>('.pane.active .cmd-block')].some((block) =>
              (block.querySelector('.cmd-output')?.textContent ?? '').includes(text),
            ),
          marker,
        ),
      { timeout: 30_000 },
    )
    .toBe(true)
  await promptReady(page)
}

/** Wait until the block whose header names `command` has painted `marker` in
 *  its OUTPUT body. The markers these specs use also appear inside the command
 *  text, so the header is not evidence that the output rows arrived: the browser
 *  can match the command a frame after Enter, before the block has any rows
 *  (nocx-4y82c.1.3, P2). */
async function outputReady(page: Page, command: string, marker: string): Promise<void> {
  await expect
    .poll(
      async () =>
        page.evaluate(
          ({ command: text, wanted }) => {
            const block = [
              ...document.querySelectorAll<HTMLElement>(
                '.pane.active .scrollback-inner > .cmd-block',
              ),
            ].find(
              (candidate) =>
                (candidate.querySelector('.cmd-header-text')?.textContent ?? '').trim() === text,
            )
            return (block?.querySelector('.cmd-output')?.textContent ?? '').includes(wanted)
          },
          { command, wanted: marker },
        ),
      { timeout: 30_000 },
    )
    .toBe(true)
}

/** The one block whose header shows `command`. */
function blockOf(reading: Reading, command: string): BlockReading {
  const block = reading.blocks.find((candidate) => candidate.command === command)
  if (!block) {
    throw new Error(
      `no block for ${JSON.stringify(command)}; saw ${JSON.stringify(reading.blocks.map((b) => b.command))}`,
    )
  }
  return block
}

test.describe('a tall block keeps its own header at the top of the pane', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 720 })
    await page.goto('/')
    await promptReady(page)
  })

  test('the header pins while the output scrolls under it, without moving a row', async ({
    page,
  }) => {
    await runCommand(page, SHORT, 'SHORT-BLOCK')
    await runCommand(page, LONG_A, 'A-END')
    await runCommand(page, LONG_B, 'B-END')

    const settled = await read(page)
    const a = blockOf(settled, LONG_A)
    const b = blockOf(settled, LONG_B)

    // UNPINNED: the block's top is below the scroller's top edge, so its
    // header scrolls with it. Nothing is pinned yet.
    const before = a.top - 60
    expect(before, 'the short block above gives the long one room to be unpinned').toBeGreaterThan(
      20,
    )
    await scrollTo(page, before)
    await expect
      .poll(async () => (await read(page)).scrollTop, { timeout: 5_000 })
      .toBe(Math.round(before * 100) / 100)
    const unpinned = await read(page)
    const unpinnedA = blockOf(unpinned, LONG_A)
    expect(unpinnedA.header, 'the long block has its header').not.toBeNull()
    expect(unpinnedA.header!.top, 'unpinned: the header is below the top edge').toBeGreaterThan(20)
    expect(unpinned.pinned, 'unpinned: nothing is pinned').toHaveLength(0)

    // PINNED: scrolling 180px further puts the block's top above the scroller
    // and its own header at the top edge.
    await scrollTo(page, before + 180)
    await expect
      .poll(
        async () => {
          const reading = await read(page)
          const header = blockOf(reading, LONG_A).header
          return header === null ? null : Math.abs(header.top)
        },
        { timeout: 5_000 },
      )
      .toBeLessThanOrEqual(TOLERANCE)
    const pinned = await read(page)
    const pinnedA = blockOf(pinned, LONG_A)

    // THE RIGHT HEADER, AT THE TOP, AND ONLY ONE.
    expect(pinnedA.header!.top).toBeLessThanOrEqual(TOLERANCE)
    expect(pinnedA.header!.top).toBeGreaterThanOrEqual(-TOLERANCE)
    expect(pinnedA.command, 'the pinned header names the command being read').toBe(LONG_A)
    expect(pinned.pinned, 'one pinned header at a time').toHaveLength(1)
    expect(pinned.pinned[0]!.command, 'and it is the block the top of the view is in').toBe(LONG_A)

    // NO ROW MOVED (ADR-2yzsq8mk, PR #752). Every row the two frames share
    // holds its place in the scroller's content, and the block's own top and
    // height are unchanged — the header is overlaying the output, not taking
    // a row of it.
    const unpinnedRows = new Map(unpinnedA.rows.map((row) => [row.text, row.y]))
    const shared = pinnedA.rows.filter((row) => unpinnedRows.has(row.text))
    expect(shared.length, 'the shared output rows are still the rows being read').toBeGreaterThan(
      20,
    )
    for (const row of shared) {
      expect(
        Math.abs(row.y - unpinnedRows.get(row.text)!),
        `row ${JSON.stringify(row.text)} kept its place in the content (unpinned ${unpinnedRows.get(row.text)}, pinned ${row.y})`,
      ).toBeLessThanOrEqual(0.5)
    }
    expect(pinnedA.top, "the block's own offset is unchanged").toBeCloseTo(unpinnedA.top, 1)
    expect(pinnedA.height, "the block's own height is unchanged").toBeCloseTo(unpinnedA.height, 1)
    // And the output still lies under the pinned header, so the header really
    // is over the block being read rather than at the top of an empty view.
    const under = pinnedA.rows.filter((row) => row.y > pinnedA.top && row.y < pinnedA.top + 400)
    expect(under.length, 'output is painted under the pinned header').toBeGreaterThan(3)

    // ITS ACTIONS WORK FROM THERE. The ⋮ lives in this same header node, so
    // opening its menu while the header is pinned proves the overlay is above
    // the rows and takes the pointer and the keyboard.
    const header = page
      .locator('.pane.active .scrollback-inner > .cmd-block')
      .filter({ hasText: 'A-START' })
      .locator(':scope > .cmd-header')
    const actions = header.locator('[data-block-actions]')
    await actions.focus()
    await page.keyboard.press('Enter')
    await expect(page.locator('[data-testid="block-actions-menu"]')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-testid="block-actions-menu"]')).toHaveCount(0)

    // ONE, AND THE NEXT ONE. Scrolling on until the second long block owns
    // the top swaps the header for that block's own.
    await scrollTo(page, b.top + 120)
    await expect
      .poll(
        async () => {
          const reading = await read(page)
          const header = blockOf(reading, LONG_B).header
          return header === null ? null : Math.abs(header.top)
        },
        { timeout: 5_000 },
      )
      .toBeLessThanOrEqual(TOLERANCE)
    const swapped = await read(page)
    expect(swapped.pinned, 'one pinned header at a time after the swap').toHaveLength(1)
    expect(swapped.pinned[0]!.command, 'the next block owns the top now').toBe(LONG_B)
    expect(
      blockOf(swapped, LONG_A).header!.top,
      "the first block's header left with its block",
    ).toBeLessThan(-100)
  })

  test('a running block pins its one-line header, and Stop works from it', async ({ page }) => {
    await clickIntoEditor(page)
    await page.keyboard.type(RUNNING)
    await page.keyboard.press('Enter')
    const running = page.locator('.pane.active .scrollback-inner > .cmd-block.cmd-block-running')
    await expect(running).toHaveCount(1, { timeout: 30_000 })
    await expect
      .poll(
        async () =>
          page.evaluate(() =>
            (document.querySelector('.pane.active')?.textContent ?? '').includes('RUN-160'),
          ),
        { timeout: 30_000 },
      )
      .toBe(true)

    const started = await read(page)
    const block = started.blocks.find((candidate) => candidate.running)
    if (!block) throw new Error('the running block is not in the scrollback')
    expect(block.header, 'the running block has its header').not.toBeNull()
    expect(block.header!.running, 'and it is the one-line running header').toBe(true)

    // Scroll up into the output: the command has printed everything and is in
    // its sleep, so the block's height is settled and the person has left the
    // tail deliberately (the app stops following at that point).
    await scrollTo(page, block.top + 120)
    await expect
      .poll(
        async () => {
          const reading = await read(page)
          const header = reading.blocks.find((candidate) => candidate.running)?.header
          return header === undefined || header === null ? null : Math.abs(header.top)
        },
        { timeout: 5_000 },
      )
      .toBeLessThanOrEqual(TOLERANCE)

    const pinned = await read(page)
    expect(pinned.pinned, 'one pinned header at a time while running').toHaveLength(1)
    expect(pinned.pinned[0]!.command, 'the pinned header names the running command').toBe(RUNNING)
    expect(pinned.pinned[0]!.running, 'and it keeps the running shape').toBe(true)

    // STOP, FROM THE PINNED HEADER. A real click: it fails if the header is
    // not the element at that point, which is the half a layout-only
    // assertion cannot see.
    const stop = page.locator(
      '.pane.active .scrollback-inner > .cmd-block.cmd-block-running > .cmd-header [data-control="stop"]',
    )
    await expect(stop).toBeVisible()
    await expect(stop).toBeEnabled()
    await stop.click()
    await expect(
      page.locator('.pane.active .scrollback-inner > .cmd-block.cmd-block-running'),
    ).toHaveCount(0, { timeout: 15_000 })
    await promptReady(page)
  })

  test('a header taller than a short pane does not pin, so its rows stay readable', async ({
    page,
  }) => {
    // A 20-line script typed as one command wraps its own header past the
    // scrollport. Pinned whole and opaque, that header covered the viewport and
    // the rows passed behind it unreadable (nocx-4y82c.1.3, P2). A header that
    // cannot be a compact part of the view does not pin at all: it scrolls away
    // as it did before this feature, so the rows stay readable, and no row
    // moves because nothing about the header's box changes.
    await page.setViewportSize({ width: 460, height: 360 })
    await page.goto('/')
    await promptReady(page)
    const units = Array.from({ length: 90 }, (_, i) => `unit-${String(i + 1).padStart(2, '0')}`)
    const command = `printf "TALL-START\\n"; printf '%s\\n' ${units.join(' ')}`
    await clickIntoEditor(page)
    await page.locator('.pane.active .nocx-editor-input').fill(command)
    await page.keyboard.press('Enter')
    await promptReady(page)
    await outputReady(page, command, 'unit-90')

    const rest = await read(page)
    const block = blockOf(rest, command)
    const header = block.header!
    // THE CASE IS REAL: the header alone is taller than the scrollport, so it
    // cannot be a compact part of any view.
    expect(header.height, 'the wrapped header is taller than the scrollport').toBeGreaterThan(
      rest.clientHeight,
    )

    // Scroll until the block's own top is above the scroller and the first
    // output rows are where the header used to be.
    const target = block.top + header.height + 10
    await scrollTo(page, target)
    await expect
      .poll(async () => (await read(page)).scrollTop, { timeout: 5_000 })
      .toBe(Math.round(target * 100) / 100)
    // The decision is made on the scroller's next animation frame, so the wait
    // is on its observable result: a header that does not pin leaves the top
    // edge with its block.
    await expect
      .poll(async () => blockOf(await read(page), command).header?.top ?? null, {
        timeout: 5_000,
      })
      .toBeLessThan(-100)
    const scrolled = await read(page)
    const scrolledBlock = blockOf(scrolled, command)

    // NOT PINNED: its header went with the block, and no header is at the top.
    expect(scrolledBlock.header!.top, 'the tall header scrolled away').toBeLessThan(-100)
    expect(scrolled.pinned, 'no header is pinned over the view').toHaveLength(0)

    // NO ROW MOVED (ADR-2yzsq8mk): the decision is a position change only, so
    // every shared row holds its place in the scroller's content.
    const restRows = new Map(block.rows.map((row) => [row.text, row.y]))
    const shared = scrolledBlock.rows.filter((row) => restRows.has(row.text))
    expect(shared.length, 'the output rows are still the rows being read').toBeGreaterThan(20)
    for (const row of shared) {
      expect(
        Math.abs(row.y - restRows.get(row.text)!),
        `row ${JSON.stringify(row.text)} kept its place in the content`,
      ).toBeLessThanOrEqual(0.5)
    }

    // WHAT A PERSON SEES, at two scroll positions along the output: the top of
    // the view is an output row itself, hit-testable and carrying its own text.
    // The stored rows begin with the wrapped echo of the command, so the walk
    // starts at the first row that is the command's own printed line.
    const rowsNow = await read(page)
    const unit = blockOf(rowsNow, command).rows.find((row) => /^unit-\d\d$/.test(row.text))
    expect(unit, 'the command printed its numbered lines').toBeDefined()
    for (const extra of [0, 260]) {
      await scrollTo(page, Math.max(0, unit!.y - 10 + extra))
      await expect
        .poll(
          async () =>
            page.evaluate(() => {
              const scroller = document.querySelector<HTMLElement>('.pane.active .scrollback-area')
              return scroller === null ? null : scroller.scrollTop
            }),
          { timeout: 5_000 },
        )
        .toBe(Math.round(Math.max(0, unit!.y - 10 + extra) * 100) / 100)
      await expect
        .poll(async () => blockOf(await read(page), command).header?.top ?? null, {
          timeout: 5_000,
        })
        .toBeLessThan(0)
      const probe = await page.evaluate((marker) => {
        const pane = document.querySelector<HTMLElement>('.pane.active')
        if (!pane) throw new Error('no active pane')
        const scroller = pane.querySelector<HTMLElement>('.scrollback-area')
        if (!scroller) throw new Error('no scroller')
        const sr = scroller.getBoundingClientRect()
        // WHAT A PERSON CAN SEE: a short window can put the scroller's own top
        // above the viewport; the visible box is the intersection.
        const left = Math.max(sr.left, 0)
        const right = Math.min(sr.right, window.innerWidth)
        const top = Math.max(sr.top, 0)
        const bottom = Math.min(sr.bottom, window.innerHeight)
        const block = [
          ...pane.querySelectorAll<HTMLElement>('.scrollback-inner > .cmd-block'),
        ].find((candidate) =>
          (candidate.querySelector('.cmd-header-text')?.textContent ?? '').includes(marker),
        )
        if (!block) throw new Error('the tall block is not in the scrollback')
        const header = block.querySelector<HTMLElement>(':scope > .cmd-header')
        if (!header) throw new Error('the tall block has no header')
        // The first row the scroller shows, and what is actually at its point.
        const row = [...block.querySelectorAll<HTMLElement>('.cmd-output .term-grid-row')].find(
          (candidate) => {
            const box = candidate.getBoundingClientRect()
            // A LINE OF THE COMMAND'S OUTPUT, not the wrapped echo of the
            // command itself, which the stored rows begin with.
            const text = (candidate.textContent ?? '').trim()
            return /^unit-\d\d$/.test(text) && box.top >= top - 0.5 && box.bottom <= bottom + 0.5
          },
        )
        const x = left + Math.min(120, (right - left) / 2)
        const rowTop = row ? Math.max(row.getBoundingClientRect().top, top) + 4 : top
        const el = row ? document.elementFromPoint(x, rowTop) : null
        return {
          headerTop: Math.round((header.getBoundingClientRect().top - sr.top) * 100) / 100,
          rowText: row ? (row.textContent ?? '').trim() : null,
          rowIsTop: el !== null && row !== undefined && row.contains(el),
        }
      }, 'TALL-START')
      expect(probe.headerTop, `the tall header stays away at +${extra}`).toBeLessThan(0)
      expect(probe.rowText, `an output row is what the view shows at +${extra}`).toMatch(
        /^unit-\d\d$/,
      )
      expect(probe.rowIsTop, `the row at the top is not covered at +${extra}`).toBe(true)
    }
  })

  test("a modal is not in the way of the resize that ends a header's pinning", async ({ page }) => {
    // P2 from the second review: the oversized-header decision used
    // elementFromPoint, so a modal's backdrop took the point and the lookup
    // found no block. Pin a wrapped command's header, open a modal, shrink the
    // window until the header is more than a third of the scroller, close the
    // modal without scrolling -- and the oversized header stayed sticky over
    // the output. The block must be found from geometry, not from pixels.
    await page.setViewportSize({ width: 1280, height: 720 })
    await page.goto('/')
    await promptReady(page)
    const words = Array.from({ length: 50 }, (_, i) => `word-${String(i + 1).padStart(2, '0')}`)
    const command = `printf "WRAP-START\\n"; printf '%s\\n' ${words.join(' ')}`
    await clickIntoEditor(page)
    await page.locator('.pane.active .nocx-editor-input').fill(command)
    await page.keyboard.press('Enter')
    await promptReady(page)
    await outputReady(page, command, 'word-50')

    const rest = await read(page)
    const block = blockOf(rest, command)
    // THE PREMISE: at this size this header legitimately pins -- it is a small
    // part of the scroller -- and there is enough output to scroll into.
    expect(
      block.header!.height,
      'the wrapped header is compact enough to pin at first',
    ).toBeLessThanOrEqual(rest.clientHeight / 3)
    expect(block.rows.length, 'the command printed rows to scroll through').toBeGreaterThan(20)
    await scrollTo(page, block.top + 120)
    await expect
      .poll(async () => blockOf(await read(page), command).header?.top ?? null, { timeout: 5_000 })
      .toBeCloseTo(0, 0)
    const pinned = await read(page)
    expect(blockOf(pinned, command).header!.pinnable, 'it is pinned at this size').toBe(true)

    // A MODAL'S BACKDROP, as a native <dialog> puts over the app: while it is
    // up, the pixels at the top of the scroller are not the transcript.
    await page.evaluate(() => {
      const veil = document.createElement('div')
      veil.id = 'w27-modal-veil'
      veil.style.cssText = 'position:fixed;inset:0;z-index:2147483647;background:rgba(0,0,0,0.4)'
      document.body.appendChild(veil)
    })
    const before = await read(page)

    // THE WINDOW SHRINKS UNDER THE MODAL, so the same header is now more than
    // a third of the scroller and may no longer pin. The backdrop stays up
    // until the new size has landed and the observer has had its frame: that
    // is the state the person is in -- the modal is open across the resize --
    // and the state in which hit testing cannot answer.
    const beforeClientHeight = await page.evaluate(
      () => document.querySelector<HTMLElement>('.pane.active .scrollback-area')!.clientHeight,
    )
    await page.setViewportSize({ width: 1280, height: 300 })
    await expect
      .poll(
        async () =>
          page.evaluate(
            () =>
              document.querySelector<HTMLElement>('.pane.active .scrollback-area')!.clientHeight,
          ),
        { timeout: 5_000 },
      )
      .toBeLessThan(beforeClientHeight)
    await page.evaluate(
      () =>
        new Promise<void>((resolve) => {
          requestAnimationFrame(() => requestAnimationFrame(() => resolve()))
        }),
    )
    await page.evaluate(() => document.getElementById('w27-modal-veil')?.remove())

    // Nothing scrolls and nothing else changes when the modal closes. The only
    // chance to re-judge was the resize, and it must not have needed a pixel.
    await expect
      .poll(async () => blockOf(await read(page), command).header?.top ?? null, {
        timeout: 5_000,
      })
      .toBeLessThan(-50)
    const after = await read(page)
    const afterBlock = blockOf(after, command)
    expect(afterBlock.header!.pinnable, 'the header may not pin at this size').toBe(false)
    expect(after.pinned, 'no header is pinned over the output').toHaveLength(0)
    expect(after.scrollTop, 'nothing scrolled').toBeCloseTo(before.scrollTop, 1)

    // AND NO ROW MOVED: the decision is a position change only.
    const beforeRows = new Map(blockOf(before, command).rows.map((row) => [row.text, row.y]))
    const shared = afterBlock.rows.filter((row) => beforeRows.has(row.text))
    expect(shared.length).toBeGreaterThan(20)
    for (const row of shared) {
      expect(
        Math.abs(row.y - beforeRows.get(row.text)!),
        `row ${JSON.stringify(row.text)} kept its place in the content`,
      ).toBeLessThanOrEqual(0.5)
    }
  })

  test('a tall frozen header that replaces a pinned running one is judged at once', async ({
    page,
  }) => {
    // P2 from the third review: the oversized-header check ran on scroll and
    // resize only. When a running block finished, block.closed replaced its
    // 32px one-line header with the tall frozen one -- after the composer's
    // resize had settled -- so the new header's size was never judged and it
    // stayed sticky over the output. The replacement itself must schedule the
    // same check, with no scroll and no resize to lean on.
    await page.setViewportSize({ width: 900, height: 720 })
    await page.goto('/')
    await promptReady(page)
    const words = Array.from({ length: 120 }, (_, i) => `late-${String(i + 1).padStart(3, '0')}`)
    const command = `printf "LATE-START\\n"; printf '%s\\n' ${words.join(' ')}; sleep 6`
    await clickIntoEditor(page)
    await page.locator('.pane.active .nocx-editor-input').fill(command)
    await page.keyboard.press('Enter')
    const running = page.locator('.pane.active .scrollback-inner > .cmd-block.cmd-block-running')
    await expect(running).toHaveCount(1, { timeout: 30_000 })
    // COMMITTED ROWS, WHILE IT RUNS. The last printed line is still in the
    // live grid, and the command text already carries every marker, so the
    // wait is on rows the block has committed to its own output.
    await expect
      .poll(
        async () =>
          page.evaluate(() => {
            const block = document.querySelector<HTMLElement>(
              '.pane.active .scrollback-inner > .cmd-block.cmd-block-running',
            )
            return block?.querySelectorAll('.cmd-output .term-grid-row').length ?? 0
          }),
        { timeout: 30_000 },
      )
      .toBeGreaterThan(30)

    const rest = await read(page)
    const block = blockOf(rest, command)
    await scrollTo(page, block.top + 100)
    await expect
      .poll(async () => blockOf(await read(page), command).header?.top ?? null, { timeout: 5_000 })
      .toBeCloseTo(0, 0)
    const pinned = await read(page)
    expect(blockOf(pinned, command).running, 'the block is still running here').toBe(true)
    expect(blockOf(pinned, command).header!.pinnable, 'the running header pins at this size').toBe(
      true,
    )

    // The command ends by itself; the freeze swaps in the tall header. Wait on
    // the swap, and on the new header being more than a third of the scroller.
    await expect
      .poll(
        async () => {
          const reading = await read(page)
          const candidate = blockOf(reading, command)
          if (candidate.running) return false
          return (candidate.header?.height ?? 0) > reading.clientHeight / 3
        },
        { timeout: 30_000 },
      )
      .toBe(true)

    // THE CONTRACT: a header taller than a third of the scroller is never the
    // pinned one. On the engine that keeps the person's scroll position that
    // means this header left the top; on the one that lands the frozen block
    // at its own top the header is simply not pinned. Both satisfy the rule
    // the fix exists for, and neither is allowed to leave an oversized header
    // holding the top edge.
    await expect
      .poll(async () => blockOf(await read(page), command).rows.length, { timeout: 10_000 })
      .toBeGreaterThan(30)
    await expect
      .poll(
        async () => {
          const reading = await read(page)
          return reading.pinned.filter((header) => header.height > reading.clientHeight / 3).length
        },
        { timeout: 5_000 },
      )
      .toBe(0)
    const after = await read(page)
    expect(after.pinned, 'no header is pinned over the output').toHaveLength(0)
    // The command's rows are still there, below where the header sits.
    expect(blockOf(after, command).rows.length).toBeGreaterThan(30)
  })
})
