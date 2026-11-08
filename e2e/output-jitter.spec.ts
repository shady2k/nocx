/**
 * A command's output does not move backwards while it prints (owner report,
 * macOS nightly: `apt update` over ssh "jumps and twitches with every new
 * line").
 *
 * The scenario is apt's shape: bursts of a few lines at a time, with a
 * progress counter rewritten in place with `\r`, over a real SSH connection
 * (`cmd/e2e-sshd`) as well as locally — the local test is the control, and it
 * says whether ssh is part of the cause.
 *
 * WHAT IS MEASURED, once per animation frame, while the command runs: the
 * block's height and top, the pane's `scrollTop`, every row visible inside the
 * scroller with its text and its position, and the stack's transform. Two
 * things a person reads as jitter are asserted:
 *
 * 1. NO ROW MOVES DOWN. The pane follows the tail, so a row of output can
 *    only be pushed UP by the lines printed below it. A row seen in two
 *    consecutive frames that moved down (by more than half a line — layout is
 *    fractional and `scrollTop` rounding leaves sub-pixel differences that
 *    nobody can see) means the output went backwards.
 * 2. ONE RUN OF ROWS, IN ORDER. The visible rows, top to bottom, are
 *    consecutive lines of the command's output: no line is painted twice and
 *    none out of order. The block's body is two sources — the rows the durable
 *    record took, and the live grid — and a frame where they overlap shows one
 *    line twice, which reads as the output jumping back.
 *
 * Both were red before their fix, and the numbers that made them red are in
 * the failure messages: the frame after every `block.grew` carried the stack
 * translated by the batch it had just committed (68, 96, 116 px measured, one
 * viewport at the worst), so the whole block sat below its own tail and slid
 * back over 140 ms while the next batch re-armed it; and once every second to
 * third frame had the committed body painted over rows the live grid still
 * showed (`GET-002, GET-003, GET-004, GET-001, GET-002…`).
 *
 * The window is the command's own: it ends at the last frame before the card
 * freezes and the composer returns, because that settle is a designed movement
 * at the end of a command, not a line arriving.
 *
 * Not asserted, and reported on the test instead: the largest sub-pixel
 * movement seen, which belongs to the block opening under a fractional
 * layout.
 */
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import type { Page } from '@playwright/test'
import { test, expect, promptReady, clickIntoEditor, resolveBackend } from './harness'
import { startSshd, rpc } from './sshd-fixture'
import { readStand } from './stand'

/**
 * The command: 240 numbered lines plus a progress counter rewritten in place
 * with `\r`, pausing every fourth line — a few hundred lines at apt's pace,
 * not as fast as the disk can write them.
 */
const COMMAND =
  'printf "APT-START\\n"; ' +
  'for i in $(seq -f "%03g" 1 240); do ' +
  'printf "GET-%s http://example.test/pool/pkg_%s_arm64.deb\\n" "$i" "$i"; ' +
  'printf "Progress %s%%\\r" "$i"; ' +
  'if [ $((10#$i % 4)) -eq 0 ]; then sleep 0.05; fi; ' +
  'done; ' +
  'printf "\\nAPT-%s-DONE\\n" END'

/** One line of the command's output, as this spec can identify it. */
const LINE = /GET-(\d{3})\b/

/** The text the pane shows only when the command has finished. */
const DONE = 'APT-END-DONE'

interface VisibleRow {
  /** Distance from the scroller's top edge, in CSS pixels. */
  y: number
  text: string
}

interface Frame {
  /** `performance.now()` when the frame was read. */
  t: number
  scrollTop: number
  /** The running block's box, relative to the scroller's top edge. */
  blockTop: number
  blockHeight: number
  /** The stack's live transform — a FLIP settle, when one is playing. */
  transform: string
  rows: VisibleRow[]
}

/**
 * Sample every animation frame until the command's last line is on screen.
 *
 * Read inside the frame the app has already painted (the sampler is its own
 * rAF callback), and never by sleeping: the stop condition is the output the
 * command produces.
 */
async function framesOf(page: Page, doneText: string): Promise<Frame[]> {
  return page.evaluate(
    async ({ doneText }) => {
      const pane = document.querySelector<HTMLElement>('.pane.active')!
      const scroller = pane.querySelector<HTMLElement>('.scrollback-area')!
      const stack = pane.querySelector<HTMLElement>('.scrollback-inner')!
      const round = (value: number): number => Math.round(value * 100) / 100
      const frames: Frame[] = []
      return new Promise<Frame[]>((resolve) => {
        const tick = (): void => {
          // ONLY WHILE THE COMMAND OWNS ITS BLOCK. The freeze swaps the running
          // card for the frozen one and gives the composer its box back, and
          // that settle is a designed movement of the whole transcript played
          // back over 140 ms — it belongs to the end of a command, not to the
          // lines arriving, which is what this spec is about. The window ends
          // at the last frame before either of those: the command is over the
          // moment the pane gives the composer its box back.
          const block = pane.querySelector<HTMLElement>('.cmd-block.cmd-block-running')
          const composer = pane.querySelector<HTMLElement>('.nocx-editor')
          const composerBack = (composer?.getBoundingClientRect().height ?? 0) > 0
          if (block === null || composerBack) {
            if (frames.length > 0) {
              resolve(frames)
              return
            }
            requestAnimationFrame(tick)
            return
          }
          const sr = scroller.getBoundingClientRect()
          const rows: VisibleRow[] = []
          for (const row of block.querySelectorAll<HTMLElement>(
            '.cmd-output .term-grid-row, .terminal-live-grid .term-grid-row',
          )) {
            const box = row.getBoundingClientRect()
            if (box.height <= 0) continue
            if (box.bottom <= sr.top + 0.5 || box.top >= sr.bottom - 0.5) continue
            rows.push({ y: round(box.top - sr.top), text: (row.textContent ?? '').trimEnd() })
          }
          const frameBox = block.getBoundingClientRect()
          frames.push({
            t: round(performance.now()),
            scrollTop: round(scroller.scrollTop),
            blockTop: round(frameBox.top - sr.top),
            blockHeight: round(frameBox.height),
            transform: getComputedStyle(stack).transform,
            rows,
          })
          if ((pane.textContent ?? '').includes(doneText)) {
            // The command's last line is on screen; the block is still running,
            // so this frame is inside the window and the next one is not.
            resolve(frames)
            return
          }
          requestAnimationFrame(tick)
        }
        requestAnimationFrame(tick)
      })
    },
    { doneText },
  )
}

interface Jitter {
  /** Rows that moved down between two frames, with the frame they moved in. */
  readonly backwards: string[]
  /** Frames whose visible rows repeat a line or carry them out of order. */
  readonly outOfOrder: string[]
  /** The largest downward movement seen, in pixels. */
  readonly worstDownPx: number
  /** The largest sub-pixel (< half a line) downward movement, reported only. */
  readonly worstSubPixelPx: number
  readonly frames: number
}

/** Judge one run's frames. `pitch` is the row height the pane paints with. */
function judge(frames: readonly Frame[], pitch: number): Jitter {
  const backwards: string[] = []
  const outOfOrder: string[] = []
  let worstDownPx = 0
  let worstSubPixelPx = 0
  for (let index = 1; index < frames.length; index += 1) {
    const previous = frames[index - 1]
    const frame = frames[index]
    // Identity is the row's TEXT, and only a LINE OF OUTPUT has one this
    // spec can name: the echo, the blank rows the grid pads with and the
    // progress counter (rewritten in place) are not rows a person reads as
    // output, and two rows with the same text say nothing about which one
    // moved. A line that appears twice in one frame is the out-of-order
    // assertion's, below.
    const keyed = (frame: Frame): Map<string, number> => {
      const counts = new Map<string, number>()
      for (const row of frame.rows) counts.set(row.text, (counts.get(row.text) ?? 0) + 1)
      const map = new Map<string, number>()
      for (const row of frame.rows) {
        if (counts.get(row.text) !== 1 || LINE.exec(row.text) === null) continue
        map.set(row.text, row.y)
      }
      return map
    }
    const before = keyed(previous)
    for (const [text, y] of keyed(frame)) {
      const was = before.get(text)
      if (was === undefined) continue
      const dy = y - was
      if (dy <= 0.05) continue
      worstDownPx = Math.max(worstDownPx, dy)
      if (dy < pitch / 2) {
        worstSubPixelPx = Math.max(worstSubPixelPx, dy)
        continue
      }
      backwards.push(
        `${dy.toFixed(2)}px down in the frame at ${frame.t}ms: ` +
          `${JSON.stringify(text.slice(0, 40))} ` +
          `(scrollTop ${previous.scrollTop} → ${frame.scrollTop}, ` +
          `block ${previous.blockTop}+${previous.blockHeight} → ${frame.blockTop}+${frame.blockHeight}, ` +
          `transform ${frame.transform})`,
      )
    }
    const ordered = frame.rows
      .map((row) => LINE.exec(row.text)?.[1])
      .filter((line): line is string => line !== undefined)
    for (let at = 1; at < ordered.length; at += 1) {
      if (Number(ordered[at]) > Number(ordered[at - 1])) continue
      outOfOrder.push(
        `the frame at ${frame.t}ms shows ${ordered.slice(Math.max(0, at - 3), at + 2).join(', ')}`,
      )
      break
    }
  }
  return {
    backwards: backwards.slice(0, 8),
    outOfOrder: outOfOrder.slice(0, 8),
    worstDownPx,
    worstSubPixelPx,
    frames: frames.length,
  }
}

/** The row pitch the pane paints with — published on the scrollback stack by
 *  scrollback/cell-metric.ts from the accepted frame's own cell height. */
async function rowPitch(page: Page): Promise<number> {
  return page.evaluate(() =>
    parseFloat(
      getComputedStyle(
        document.querySelector<HTMLElement>('.pane.active .scrollback-inner')!,
      ).getPropertyValue('--term-cell-height'),
    ),
  )
}

/**
 * Type the command in the pane's editor, sample while it prints, and assert
 * the two invariants.
 */
async function expectNoJitter(page: Page): Promise<void> {
  await clickIntoEditor(page)
  await page.keyboard.type(COMMAND)
  const sampling = framesOf(page, DONE)
  await page.keyboard.press('Enter')
  const frames = await sampling
  const jitter = judge(frames, 17)
  const said = `output jitter: ${JSON.stringify(jitter)}`
  test.info().annotations.push({
    type: 'output jitter (frames sampled, worst sub-pixel move)',
    description: `${jitter.frames} frames, worst reported sub-pixel move ${jitter.worstSubPixelPx.toFixed(2)}px`,
  })
  // The command's lines arrived as frames — a run of a dozen or two is what
  // the pace above produces on both engines; anything far short of that means
  // the measurement, not the product, is what failed.
  expect(jitter.frames, said).toBeGreaterThan(30)
  expect(jitter.backwards, said).toEqual([])
  expect(jitter.outOfOrder, said).toEqual([])
}

test('a local command printing at apt pace never moves its output backwards', async ({ page }) => {
  test.setTimeout(120_000)
  await page.goto('/')
  await promptReady(page)
  expect(await rowPitch(page)).toBeGreaterThan(0)
  await expectNoJitter(page)
})

test('a command printing at apt pace over ssh never moves its output backwards', async ({
  page,
}) => {
  test.setTimeout(180_000)
  // The e2e suite's SSH stand: a real sshd (`cmd/e2e-sshd`) that authenticates,
  // runs bash on a real PTY and serves the real filesystem. The remote HOME and
  // cwd are its own, so the pane's shell sources nothing the local backend
  // installed.
  const remoteHome = mkdtempSync(path.join(tmpdir(), 'jitter-remote-'))
  const remoteCwd = mkdtempSync(path.join(tmpdir(), 'jitter-cwd-'))
  const fixture = await startSshd({ home: remoteHome, cwd: remoteCwd })
  let createdId: string | null = null
  try {
    // Seed the isolated home's known_hosts so the backend's ssh client accepts
    // the fixture's host key. REPLACED, not appended: every fixture spawn mints
    // fresh keys.
    const sshDir = path.join(readStand().home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), fixture.knownHosts + '\n')
    await page.goto('/')
    await promptReady(page)
    const wsInfo = await resolveBackend(page)
    const profileName = `jitter-fixture-${Date.now()}`
    const created = await rpc<{ id: string }>(page, wsInfo, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: fixture.user,
        keyPath: fixture.userKey,
        desiredMode: 'script',
      },
    })
    createdId = created.id
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible()
    await search.fill(profileName)
    // Wait for the saved row before Enter: the palette renders its search box
    // before its providers answer, and Enter on an empty list reaches the
    // ad-hoc fallback with no credential (nocx-k1691).
    await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
      timeout: 10_000,
    })
    await page.keyboard.press('Enter')
    await expect(page.locator('.pane.active .nocx-editor-input')).toBeVisible({ timeout: 30_000 })
    expect(await rowPitch(page)).toBeGreaterThan(0)
    await expectNoJitter(page)
  } finally {
    try {
      const info = await resolveBackend(page)
      if (createdId !== null) await rpc(page, info, 'profiles.delete', { id: createdId })
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
    fixture.proc.kill('SIGKILL')
  }
})

/** One burst loop: ten rows every 300 ms — the owner's own reproducer. */
const BURST =
  'printf "BURST-START\\n"; ' +
  'for i in $(seq 1 24); do seq $((i*10)) $((i*10+9)); sleep 0.3; done; ' +
  'printf "BURST-%s-END\\n" DONE'

interface ShapeFrame {
  t: number
  scrollTop: number
  scrollH: number
  clientH: number
  blockLeft: number
  blockRight: number
  blockH: number
  composerH: number
  /** The running block's rows and the box that holds them. */
  liveTop: number
  liveBottom: number
  rowX: number
  rowRight: number
  /** Row boundaries inside the live box, to say whether a row is cut. */
  cutRows: number
  outRows: number
  /** The frozen block, once the command ends. */
  frozenLeft: number
  frozenRowX: number
  frozenRowRight: number
  /** A frame where the block's own room changed without its rows changing. */
  roomWithoutRows: number
  /** The pitch two consecutive live rows are painted at, and the box's height
   *  as a multiple of it — the two numbers that make a cut row possible. */
  pitch: number
  boxRows: number
  cutText: string
  viewportBottom: number
  viewportRows: number
}

/**
 * What the owner's frames have to say, one animation frame at a time: the
 * block's box and each visible row's left and right edges, the live box's
 * bounds against the rows painted inside it, and — after the freeze — the
 * frozen block's own left and row edges.
 */
async function shapeFrames(page: Page): Promise<ShapeFrame[]> {
  return page.evaluate(async () => {
    const r2 = (v: number): number => Math.round(v * 100) / 100
    const pane = document.querySelector<HTMLElement>('.pane.active')!
    const scroller = pane.querySelector<HTMLElement>('.scrollback-area')!
    const frames: ShapeFrame[] = []
    let previous: { rows: number; height: number } | null = null
    return new Promise<ShapeFrame[]>((resolve) => {
      const tick = (): void => {
        const block = pane.querySelector<HTMLElement>('.cmd-block.cmd-block-running')
        const frozen = pane.querySelector<HTMLElement>('.cmd-block:not(.cmd-block-running)')
        const live = pane.querySelector<HTMLElement>('.terminal-live-container')
        const composer = pane.querySelector<HTMLElement>('.nocx-editor')
        const sr = scroller.getBoundingClientRect()
        const box = (el: Element | null): DOMRect | null => (el ? el.getBoundingClientRect() : null)
        const br = box(block)
        const lr = box(live)
        const output = block?.querySelector<HTMLElement>(':scope > .cmd-output') ?? null
        const outRows = output ? [...output.querySelectorAll<HTMLElement>('.term-grid-row')] : []
        const liveRows = [
          ...(pane.querySelectorAll<HTMLElement>('.terminal-live-grid .term-grid-row') ?? []),
        ]
        const viewport = pane.querySelector<HTMLElement>('.terminal-live-viewport')
        const vr = viewport ? viewport.getBoundingClientRect() : null
        // Rows painted INSIDE the live box: its own rows, clipped by it.
        // The CLIP is the viewport — `overflow: hidden` on the box the grid is
        // drawn into. The container around it pays the body's padding, so a
        // row's 4px inside the container is padding, not a sliced row.
        let cutRows = 0
        for (const row of liveRows) {
          const b = row.getBoundingClientRect()
          const clip = vr ?? lr
          if (clip === null || b.height <= 0) continue
          if (b.top >= clip.bottom - 0.5) continue
          if (b.bottom > clip.bottom + 0.5 && b.top < clip.bottom - 0.5) cutRows += 1
        }
        let pitch = NaN
        for (let i = 1; i < liveRows.length; i += 1) {
          const a = liveRows[i - 1].getBoundingClientRect()
          const b = liveRows[i].getBoundingClientRect()
          if (b.top > a.top + 0.5) {
            pitch = Math.round((b.top - a.top) * 100) / 100
            break
          }
        }
        let cutText = ''
        if (lr !== null) {
          for (const row of liveRows) {
            const b = row.getBoundingClientRect()
            if (b.top < lr.bottom - 0.5 && b.bottom > lr.bottom + 0.5) {
              cutText = `${lr.bottom - b.top} of ${b.height}: ${(row.textContent ?? '').slice(0, 12)}`
              break
            }
          }
        }
        const firstRow = outRows[0] ?? liveRows[0] ?? null
        const firstRect = box(firstRow)
        const fRow =
          frozen?.querySelector<HTMLElement>(':scope > .cmd-output .term-grid-row') ?? null
        const fRect = box(frozen)
        const fRowRect = box(fRow)
        const rowsNow = outRows.length
        const heightNow = br ? br.height : NaN
        // A block whose room changed while its painted rows did not is the
        // "room first, rows later" frame the owner saw as the scrollbar
        // jumping before the lines arrived.
        let roomWithoutRows = 0
        if (previous !== null && br !== null && rowsNow === previous.rows) {
          const grew = Math.abs(heightNow - previous.height) > 0.5
          if (grew) roomWithoutRows = 1
        }
        if (br !== null) previous = { rows: rowsNow, height: heightNow }
        // THE WINDOW CLOSES ON THE EVENT ITSELF — the frozen body's own first
        // painted row, in the frame that records it. Not the composer coming
        // back: a frame can pass between the composer's return and the card's
        // first row, and a sampler that closes there hands the test an empty
        // frozen column under load. And never a deadline: the row is the
        // observable, so the sampler waits for the row.
        const frozenRowPainted =
          frozen !== null &&
          fRowRect !== null &&
          (frozen.textContent ?? '').includes('BURST-DONE-END')
        frames.push({
          t: r2(performance.now()),
          scrollTop: r2(scroller.scrollTop),
          scrollH: r2(scroller.scrollHeight),
          clientH: r2(scroller.clientHeight),
          blockLeft: br ? r2(br.left - sr.left) : NaN,
          blockRight: br ? r2(br.right - sr.left) : NaN,
          blockH: br ? r2(br.height) : NaN,
          composerH: composer ? r2(composer.getBoundingClientRect().height) : 0,
          liveTop: lr ? r2(lr.top - sr.top) : NaN,
          liveBottom: lr ? r2(lr.bottom - sr.top) : NaN,
          rowX: firstRect ? r2(firstRect.left - sr.left) : NaN,
          rowRight: firstRect ? r2(firstRect.right - sr.left) : NaN,
          cutRows,
          outRows: rowsNow,
          frozenLeft: fRect ? r2(fRect.left - sr.left) : NaN,
          frozenRowX: fRowRect ? r2(fRowRect.left - sr.left) : NaN,
          frozenRowRight: fRowRect ? r2(fRowRect.right - sr.left) : NaN,
          roomWithoutRows,
          pitch,
          boxRows: lr !== null && pitch > 0 ? r2(lr.height / pitch) : NaN,
          cutText,
          viewportBottom: vr ? r2(vr.bottom - sr.top) : NaN,
          viewportRows: vr !== null && pitch > 0 ? r2(vr.height / pitch) : NaN,
        })
        if (frozenRowPainted) {
          resolve(frames)
          return
        }
        requestAnimationFrame(tick)
      }
      requestAnimationFrame(tick)
    })
  })
}

test('a burst of rows arrives whole, in the block it will keep', async ({ page }) => {
  test.setTimeout(180_000)
  await page.goto('/')
  await promptReady(page)
  await clickIntoEditor(page)
  await page.keyboard.type(BURST)
  const sampling = shapeFrames(page)
  await page.keyboard.press('Enter')
  const frames = await sampling
  const running = frames.filter((f) => f.outRows > 0 && f.composerH === 0)
  const spoken = `shape: ${JSON.stringify(running.slice(-3))}`
  expect(running.length, spoken).toBeGreaterThan(20)

  // ONE SHAPE. Every row of the running block starts and ends where the frozen
  // block's rows do: the live row and the finished row are one column.
  const xs = new Set(running.map((f) => f.rowX))
  const rights = new Set(running.map((f) => f.rowRight))
  const frozenX = frames.filter((f) => f.frozenRowX === f.frozenRowX).map((f) => f.frozenRowX)
  const frozenRight = frames
    .filter((f) => f.frozenRowRight === f.frozenRowRight)
    .map((f) => f.frozenRowRight)
  expect(frozenX.length, spoken).toBeGreaterThan(0)
  expect([...xs], spoken).toEqual([frozenX[frozenX.length - 1]])
  expect([...rights], spoken).toEqual([frozenRight[frozenRight.length - 1]])
  expect(new Set(running.map((f) => f.blockLeft)).size, spoken).toBe(1)

  // NO ROOM WITHOUT ITS ROWS: the block never grew in a frame where its own
  // painted rows stayed the same — the room and the rows are one paint.
  expect(
    running.filter((f) => f.roomWithoutRows === 1),
    spoken,
  ).toEqual([])

  // NOTHING CUT: no frame shows a row sliced by the bottom of the live box.
  expect(
    frames.filter((f) => f.cutRows > 0),
    spoken,
  ).toEqual([])

  // THE BOX NEVER OUTGROWS THE PANE it is drawn in: a live box taller than the
  // scroller is the grid cut short mid-row, which the owner photographed at
  // the hand-off.
  expect(
    frames.filter((f) => f.liveBottom - f.liveTop > f.clientH + 0.5),
    spoken,
  ).toEqual([])
})
