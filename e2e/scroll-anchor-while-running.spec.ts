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

/** A Node program on disk — for the shapes a shell cannot write safely: an
 *  escape that changes the screen, written from bytes the program owns. */
function script(name: string, lines: string[]): string {
  const dir = mkdtempSync(path.join(tmpdir(), `nocx-${name}-`))
  const file = path.join(dir, 'producer.mjs')
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

  // The producer: still printing while the person reads, AND HELD AT ITS NEXT
  // OUTPUT UNTIL THE BASELINE IS TAKEN (nocx-3wmve). Without the gate the whole
  // run can be over before the wheel and the capture finish, and the assertion
  // then compares two readings of the same finished transcript — a pass that
  // observed no arriving output at all.
  const release = path.join(mkdtempSync(path.join(tmpdir(), 'nocx-live-')), 'go')
  const file = producer('live', [
    'for i in $(seq -f "%03g" 1 60); do echo LIVE-$i; sleep 0.1; done',
    `while [ ! -f ${release} ]; do sleep 0.05; done`,
    'for i in $(seq -f "%03g" 61 120); do echo LIVE-$i; sleep 0.1; done',
    'echo LIVE-DONE',
  ])
  await page.keyboard.type(`bash ${file}`)
  await page.keyboard.press('Enter')
  await waitForText(page, 'LIVE-060')

  await wheelIntoHistory(page, /HIST-070/)
  const before = await readerPlace(page)
  const topBefore = await scrollerTop(page)
  // THE PREMISE, STATED: the producer is still waiting, so the lines this case
  // is about have not arrived yet.
  expect(await page.evaluate(() => (document.body.textContent ?? '').includes('LIVE-120'))).toBe(
    false,
  )
  writeFileSync(release, 'go')

  // Sixty more lines arrive; the person has not scrolled.
  await waitForText(page, 'LIVE-120')
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
  //
  // THE END IS THE TEST'S TO RELEASE, NOT A WINDOW'S (nocx-3wmve). A command
  // that ends on a timer can finish before this test has wheeled and captured
  // its baseline — and then the baseline is already the frozen card, the wait
  // below returns at once, and unchanged rows pass without a freeze ever being
  // observed. The producer blocks on a file this test writes after it has the
  // running-state baseline, so the sequence is the assertion's, not a race's.
  const release = path.join(mkdtempSync(path.join(tmpdir(), 'nocx-release-')), 'go')
  const file = producer('release', [
    'for i in $(seq -f "%03g" 1 300); do echo FAST-$i; done',
    `while [ ! -f ${release} ]; do sleep 0.05; done`,
    'echo FAST-DONE',
  ])
  await page.keyboard.type(`bash ${file}`)
  await page.keyboard.press('Enter')
  await waitForText(page, 'FAST-250')

  await wheelIntoHistory(page, /FAST-1\d\d/)
  const before = await readerPlace(page)
  const topBefore = await scrollerTop(page)
  // THE BASELINE IS THE RUNNING CARD'S, and these are the lines that say so:
  // the block is still running when it is read, and its identity is taken here
  // so the after-measurement can wait for THAT card to freeze.
  const running = page.locator('.pane.active .cmd-block-running')
  await expect(running).toHaveCount(1)
  const entryId = await running.getAttribute('data-entry-id')
  expect(entryId, 'the running block carries no entry id to wait on').not.toBeNull()

  writeFileSync(release, 'go')
  // NOT promptReady ALONE (nocx-3wmve): the composer returns when the prompt
  // is ready, and the VISUAL swap waits for the backend's block.closed, which
  // can land later — measuring then would read the running card's own rows and
  // call a freeze observed that nobody had seen. The frozen element is the
  // command's own block without `cmd-block-running`.
  await promptReady(page)
  await expect(
    page.locator(`.pane.active .cmd-block[data-entry-id="${entryId}"]:not(.cmd-block-running)`),
  ).toHaveCount(1)
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
  // The redraws are HELD AFTER A SHORT FIRST REGION UNTIL THE BASELINE IS TAKEN
  // (nocx-3wmve): sixty redraws at a fifth of a second can be over before the
  // wheel and the capture finish, and the case would then compare two readings
  // of a program that had stopped drawing — a pass that observed no redraw.
  const release = path.join(mkdtempSync(path.join(tmpdir(), 'nocx-region-')), 'go')
  const file = producer('region', [
    'for i in $(seq -f "%03g" 1 120); do echo REGION-HIST-$i; done',
    'height=0',
    'draw() {',
    '  if [ "$height" -gt 0 ]; then printf "\\033[${height}A\\033[J"; fi',
    '  printf "REGION-HDR-%03d\\n" "$1"',
    '  if [ $(( $1 % 2 )) -eq 0 ]; then rows=28; else rows=2; fi',
    '  r=1',
    '  while [ "$r" -lt "$rows" ]; do printf "region row %s\\n" "$r"; r=$((r + 1)); done',
    '  height=$rows',
    '}',
    'draw 1; draw 2; draw 3',
    'sleep 0.2',
    `while [ ! -f ${release} ]; do sleep 0.05; done`,
    'for i in $(seq 4 60); do draw "$i"; sleep 0.2; done',
    'echo REGION-DONE',
  ])

  await page.goto('/')
  await promptReady(page)
  await clickIntoEditor(page)
  await page.keyboard.type(`bash ${file}`)
  await page.keyboard.press('Enter')
  await waitForText(page, 'REGION-HIST-120')

  await wheelIntoHistory(page, /REGION-HIST-\d{3}/, -300)
  const before = await readerPlace(page)
  const topBefore = await scrollerTop(page)
  // THE PREMISE, STATED: the producer is still waiting, so the redraws this
  // case is about have not happened yet.
  expect(await page.evaluate(() => (document.body.textContent ?? '').includes('REGION-DONE'))).toBe(
    false,
  )
  writeFileSync(release, 'go')

  // The program redraws its region sixty times; nothing the person does in
  // between may move them.
  await waitForText(page, 'REGION-DONE')
  const after = await readerPlace(page)
  const topAfter = await scrollerTop(page)

  expectUnmoved(before, after, 'an inline program redrawing its region', {
    before: topBefore,
    after: topAfter,
  })
})

// NOT FIXED HERE; ITS OWN BEAD (nocx-4y82c.1.9). A program that owns the pane
// takes the transcript out of the flow, the scroller loses its position, and the
// follow sentinel then reports "the live end" for a reader who never moved —
// measured: scrollTop 1062 before the program, 1644 after it, and 1644 is
// exactly scrollHeight - clientHeight. The fixes on this branch hold a reader
// across a *freeze*; they do not hold one across the pane being handed to a
// program and back, which needs the follow intent itself to be answered
// differently for a transcript that is out of the flow (nocx-4y82c.1.9). The
// assertion stays in the tree, disabled, so whoever fixes it removes one line.
test.fixme('a full-screen program leaves a reader in history where they were', async ({ page }) => {
  test.setTimeout(180_000)
  const file = script('fullscreen', [
    'const { stdin, stdout } = process',
    'const sleep = (ms) => new Promise((r) => setTimeout(r, ms))',
    ';(async () => {',
    "  for (let i = 0; i < 120; i++) stdout.write('FULL-HIST-' + String(i).padStart(3, '0') + '\\n')",
    '  await sleep(1500)',
    '  stdin.setRawMode(true)',
    "  stdout.write('\\x1b[?1049h\\x1b[?1000h\\x1b[?1006h\\x1b[2J\\x1b[5;1HFULL-READY')",
    '  for (let i = 0; i < 10; i++) {',
    "    stdout.write('\\x1b[H\\x1b[2JFULL-FRAME-' + String(i).padStart(3, '0') + '\\nrow a\\nrow b\\n')",
    '    await sleep(150)',
    '  }',
    "  stdout.write('\\x1b[?1000l\\x1b[?1006l\\x1b[?1049l')",
    "  stdout.write('FULL-DONE\\n')",
    '})()',
  ])

  await page.goto('/')
  await promptReady(page)
  await clickIntoEditor(page)
  await page.keyboard.type(`node ${file}`)
  await page.keyboard.press('Enter')
  await waitForText(page, 'FULL-HIST-119')

  await wheelIntoHistory(page, /FULL-HIST-\d{3}/)
  const before = await readerPlace(page)
  const topBefore = await scrollerTop(page)

  // The program takes the pane, redraws a screen of its own, and gives the
  // pane back. None of that is the person asking to be moved.
  await expect(page.locator('.pane.active .terminal-live-container')).toHaveClass(
    /live-fullscreen/,
    { timeout: 30_000 },
  )
  await waitForText(page, 'FULL-DONE')
  await promptReady(page)

  const after = await readerPlace(page)
  const topAfter = await scrollerTop(page)
  expectUnmoved(before, after, 'a full-screen program over a reader in history', {
    before: topBefore,
    after: topAfter,
  })
})
