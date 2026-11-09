/**
 * e2e: new output never moves a reader who has scrolled away (nocx-3wmve,
 * owner report #691).
 *
 * The person wheels up from the live end into the middle of a running
 * command's history. From then on, output arriving — the lines themselves and
 * the moment the command finishes and its card freezes — may not move the rows
 * they are reading, and may not re-engage following.
 *
 * Measured on what a person sees: every row intersecting the scroller's own
 * rectangle with its distance from the scroller's top edge, never a mounted-row
 * count and never a scroller offset alone. The rows are identified by their
 * printed text, so a row that survived the change is found wherever it is; the
 * assertion is that it is found in the same place.
 */
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { test, expect, promptReady, clickIntoEditor } from './harness'
import type { Page } from './harness'

const SCROLLER = '.pane.active .scrollback-area'

/** One visible row: its printed text and where it starts, from the scroller's
 *  own top edge, in CSS pixels. */
interface VisibleRow {
  text: string
  y: number
}

async function visibleRows(page: Page): Promise<VisibleRow[]> {
  return page.evaluate((selector) => {
    const scroller = document.querySelector<HTMLElement>(selector)
    if (scroller === null) throw new Error('scrollback scroller is missing')
    const clip = scroller.getBoundingClientRect()
    const visible: Array<{ text: string; y: number }> = []
    for (const row of scroller.querySelectorAll<HTMLElement>(
      '.cmd-block .cmd-output .term-grid-row, .terminal-live-inner .term-grid-row',
    )) {
      const box = row.getBoundingClientRect()
      if (box.height <= 0) continue
      if (box.bottom <= clip.top + 0.5 || box.top >= clip.bottom - 0.5) continue
      visible.push({
        text: (row.textContent ?? '').trimEnd(),
        y: Math.round((box.top - clip.top) * 100) / 100,
      })
    }
    return visible
  }, SCROLLER)
}

/** The reader's place, stated as a row near the TOP of the viewport and where
 *  that row sits on the glass, with the rows immediately around it — the
 *  thing a person is reading. A row at the top edge is the strongest witness:
 *  the viewport's own height may change with the composer, and a row that
 *  stays in view must stay at the same distance from that edge. */
interface ReaderPlace {
  anchor: string
  y: number
  /** The rows within two of the anchor, top to bottom. */
  window: Array<{ text: string; y: number }>
  /** Everything visible, so a row that moved can still be found. */
  all: VisibleRow[]
}

function placeOf(rows: VisibleRow[]): ReaderPlace {
  const index = rows.findIndex((row) => row.y >= 0)
  const anchor = rows[index]
  if (anchor === undefined) throw new Error('nothing is visible to anchor on')
  return {
    anchor: anchor.text,
    y: anchor.y,
    window: rows.slice(Math.max(0, index - 2), index + 3),
    all: rows,
  }
}

/** Where the reader is now, and what they see there. */
async function readerPlace(page: Page): Promise<ReaderPlace> {
  return placeOf(await visibleRows(page))
}

async function scrollerTop(page: Page): Promise<number> {
  return page.evaluate(
    (selector) => document.querySelector<HTMLElement>(selector)!.scrollTop,
    SCROLLER,
  )
}

async function scrollState(page: Page): Promise<{ top: number; height: number; client: number }> {
  return page.evaluate((selector) => {
    const scroller = document.querySelector<HTMLElement>(selector)!
    return {
      top: Math.round(scroller.scrollTop),
      height: Math.round(scroller.scrollHeight),
      client: scroller.clientHeight,
    }
  }, SCROLLER)
}

async function waitForText(page: Page, text: string, timeout = 60_000): Promise<void> {
  await expect
    .poll(
      () =>
        page.evaluate(
          (needle) => (document.querySelector('.pane.active')?.textContent ?? '').includes(needle),
          text,
        ),
      { timeout },
    )
    .toBe(true)
}

/** Wheel up until a row carrying `pattern` is on screen AND the reader is
 *  genuinely away from the live end (a row of the live screen is not history).
 *  The wheel's own deliveries are not the subject; the position is. */
async function wheelIntoHistory(page: Page, pattern: RegExp, delta = -400): Promise<void> {
  await page.locator(SCROLLER).hover()
  await expect
    .poll(
      async () => {
        const rows = await visibleRows(page)
        const scroller = await scrollState(page)
        const away = scroller.top < scroller.height - scroller.client - 200
        if (away && rows.some((row) => pattern.test(row.text))) return true
        await page.mouse.wheel(0, delta)
        return false
      },
      { timeout: 30_000, intervals: [200] },
    )
    .toBe(true)
}

/** The rows the reader was reading are still there, in the same place. */
function expectUnmoved(
  before: ReaderPlace,
  after: ReaderPlace,
  note: string,
  tops?: { before: number; after: number },
): void {
  const moved_ = tops === undefined ? '' : ` (scrollTop ${tops.before} -> ${tops.after})`
  const anchorNow = after.all.find((row) => row.text === before.anchor)
  expect(
    anchorNow,
    `${note}: the row ${JSON.stringify(before.anchor)} left the viewport${moved_}`,
  ).toBeDefined()
  if (anchorNow !== undefined) {
    expect(
      Math.abs(anchorNow.y - before.y),
      `${note}: the row ${JSON.stringify(before.anchor)} moved from y=${before.y} to y=${anchorNow.y}${moved_}`,
    ).toBeLessThanOrEqual(1)
  }
  const moved = before.window
    .map((row) => {
      const now = after.all.find((candidate) => candidate.text === row.text)
      if (now === undefined) return `${row.text}: gone from the viewport`
      if (Math.abs(now.y - row.y) <= 1) return null
      return `${row.text}: y ${row.y} -> ${now.y}`
    })
    .filter((entry): entry is string => entry !== null)
  expect(moved, `${note}: ${moved.join('; ')}${moved_}`).toEqual([])
}

/** A producer script on disk: its bytes are its own, never typed through an
 *  editor that would interpret an escape as a key. */
function producer(name: string, lines: string[]): string {
  const dir = mkdtempSync(path.join(tmpdir(), `nocx-${name}-`))
  const file = path.join(dir, 'producer.sh')
  writeFileSync(file, lines.join('\n') + '\n', { mode: 0o755 })
  return file
}

test('arriving output does not move a reader scrolled into history', async ({ page }) => {
  test.setTimeout(180_000)
  await page.goto('/')
  await promptReady(page)

  // The history: one finished command whose rows a person can identify.
  await clickIntoEditor(page)
  await page.keyboard.type('for i in $(seq -f "%03g" 1 120); do echo HIST-$i; done')
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(0, { timeout: 60_000 })
  await promptReady(page)

  // The producer: still printing while the person reads.
  await page.keyboard.type(
    'for i in $(seq -f "%03g" 1 60); do echo LIVE-$i; sleep 0.12; done; echo LIVE-DONE',
  )
  await page.keyboard.press('Enter')
  await waitForText(page, 'LIVE-005')

  await wheelIntoHistory(page, /HIST-070/)
  const before = await readerPlace(page)
  const topBefore = await scrollerTop(page)

  // Several more lines arrive; the person has not scrolled.
  await waitForText(page, 'LIVE-040')
  const after = await readerPlace(page)
  const topAfter = await scrollerTop(page)

  expectUnmoved(before, after, 'output arriving under a reader in history', {
    before: topBefore,
    after: topAfter,
  })
})

test('a command ending does not move a reader scrolled into its own output', async ({ page }) => {
  test.setTimeout(180_000)
  await page.goto('/')
  await promptReady(page)

  await clickIntoEditor(page)
  await page.keyboard.type('for i in $(seq -f "%03g" 1 150); do echo HIST-$i; done')
  await page.keyboard.press('Enter')
  await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(0, { timeout: 60_000 })
  await promptReady(page)

  // A long command: the person reads the middle of ITS output, and then it
  // ends. The last lines arriving and the card freezing are output arriving —
  // nothing about them is the person asking to be moved.
  await page.keyboard.type('for i in $(seq -f "%03g" 1 300); do echo FAST-$i; done; sleep 3')
  await page.keyboard.press('Enter')
  await waitForText(page, 'FAST-250')

  await wheelIntoHistory(page, /FAST-1\d\d/)
  const before = await readerPlace(page)
  const topBefore = await scrollerTop(page)

  await promptReady(page)
  const after = await readerPlace(page)
  const topAfter = await scrollerTop(page)

  expectUnmoved(before, after, 'the command ending under a reader in its output', {
    before: topBefore,
    after: topAfter,
  })

  // The view is still the person's: scrolling works from here.
  await page.locator(SCROLLER).hover()
  await page.mouse.wheel(0, -400)
  await expect.poll(() => scrollerTop(page), { timeout: 10_000 }).toBeLessThan(topAfter)
})

test('an inline program redrawing its region does not move a reader in history', async ({
  page,
}) => {
  test.setTimeout(180_000)
  const file = producer('region', [
    'for i in $(seq -f "%03g" 1 120); do echo REGION-HIST-$i; done',
    'height=0',
    'for i in $(seq 1 40); do',
    '  if [ "$height" -gt 0 ]; then printf "\\033[${height}A\\033[J"; fi',
    '  printf "REGION-HDR-%03d\\n" "$i"',
    '  if [ $((i % 2)) -eq 0 ]; then rows=28; else rows=2; fi',
    '  r=1',
    '  while [ "$r" -lt "$rows" ]; do printf "region row %s\\n" "$r"; r=$((r + 1)); done',
    '  height=$rows',
    '  sleep 0.12',
    'done',
    'echo REGION-DONE',
  ])

  await page.goto('/')
  await promptReady(page)
  await clickIntoEditor(page)
  await page.keyboard.type(`bash ${file}`)
  await page.keyboard.press('Enter')
  await waitForText(page, 'REGION-HDR-004')

  await wheelIntoHistory(page, /REGION-HIST-\d{3}/, -300)
  const before = await readerPlace(page)
  const topBefore = await scrollerTop(page)

  // The program redraws its region forty times; nothing the person does in
  // between may move them.
  await waitForText(page, 'REGION-DONE')
  const after = await readerPlace(page)
  const topAfter = await scrollerTop(page)

  expectUnmoved(before, after, 'an inline program redrawing its region', {
    before: topBefore,
    after: topAfter,
  })
})
