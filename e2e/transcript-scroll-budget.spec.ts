/**
 * e2e: measure a long command transcript in the real app (nocx-zg3k3.2.10).
 *
 * The transcript is built LIVE, not restored: restore-client.ts deliberately
 * caps a reload at 50 blocks. Each command emits 100 short rows, below the
 * history.outputCapKB limit, so this session contains 500 blocks and 50,000
 * stored rows without exercising the per-command cap.
 *
 * The benchmark collects requestAnimationFrame intervals while the real
 * scrollback container moves from top to bottom. It also records the native
 * selection and browser find observations separately. A missing search
 * capability therefore cannot be hidden by making the frame benchmark pass.
 */
import { expect } from '@playwright/test'
import type { Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  appReadyForInput,
  bindEndpoint,
  openControlPlane,
  VaultBackend,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'
import { judgeFrames, type FrameVerdict } from './frame-budget.mts'

const serverBin = () => readStand().server

const BLOCKS = 500
const ROWS_PER_BLOCK = 100
// The transcript's own last row: the off-screen half of the native find probe.
// Derived from BLOCKS, never spelled out — a reduced run is the same spec.
const LAST_MARKER = `transcript-${String(BLOCKS).padStart(4, '0')}-${String(ROWS_PER_BLOCK).padStart(3, '0')}`
const FRAME_SAMPLES = 180
// Idle frames sampled just before the scroll, in the same page, with nothing
// moving: the display interval the scroll is judged against. Why it cannot come
// from the scroll itself, and the criterion, are in e2e/frame-budget.mts.
const IDLE_SAMPLES = 120
const INPUT = '.pane.active .nocx-editor-input'
const BLOCK = '.pane.active .scrollback-inner > .cmd-block:not(.cmd-block-running)'
const RUNNING_BLOCK = '.pane.active .scrollback-inner > .cmd-block.cmd-block-running'
const SCROLLER = '.pane.active .scrollback-area'
const ROW = `${BLOCK} .cmd-output .term-grid-row`
interface TranscriptMetrics {
  blocks: number
  rows: number
  domNodes: number
  scrollHeight: number
  clientHeight: number
  maxScrollTop: number
  idleFrames: number[]
  scrollFrames: number[]
}

interface SelectionEvidence {
  crossBlockTextLength: number
  crossBlockIncludesFirst: boolean
  crossBlockIncludesSecond: boolean
  visibleBlockText: string
  visibleBlockInViewport: boolean
}

interface FindEvidence {
  visibleFound: boolean
  visibleBeforeSearch: boolean
  offscreenFound: boolean
  offscreenBeforeSearch: boolean
}

const test = base

// THE TRACE KEEPS ITS ACTION LOG AND LOSES ITS DOM SNAPSHOTS (nocx-2v80t.3.35).
// Playwright's tracer serialises the whole document before and after every
// action, and this spec takes a few thousand actions over a document that grows
// to 60,000 nodes: profiled in Chromium at 158 blocks, the snapshotter was 3.5 s
// of 7.5 s of main-thread time across eight commands, and it grew with every
// block — the harness was making each command cost more than the last. The
// failure-context block (e2e/failure-context.ts) takes its own ARIA snapshot at
// the moment of failure, so nothing a failure needs is lost.
test.use({ trace: { mode: 'retain-on-failure', snapshots: false } })

test.describe('long transcript scroll budget', () => {
  // 500 blocks means 500 real PTY round trips (fill, Enter, wait for freeze),
  // each paying full backend + browser overhead in the container. What a block
  // costs must not grow with the blocks above it, and it did: 0.19 s at block 1
  // and 1.12 s at block 276 in Chromium, 3.5 s at block 273 in WebKit, which
  // ran the build past this budget in both browsers in CI (nocx-2v80t.3.35).
  // Three causes, each measured and removed at its owner: the trace snapshots
  // above and the wait below (harness), a settle that toggled the scroller's
  // overflow (scrollback/controller.ts `_glide`) and xterm's DOM renderer
  // rewriting identical stylesheets (renderers/xterm.ts
  // `holdIdenticalStyleText`) — each of the last two restyled every block in
  // the transcript, several times per command. 16 minutes stays: it bounds a
  // real per-block stall, still bounded per block by the 60 s wait below.
  test.setTimeout(16 * 60_000)

  let home: DisposableRoot
  let backend: VaultBackend
  test.beforeEach(() => {
    home = { root: mkdtempSync(join(tmpdir(), 'nocx-transcript-budget-')) }
    backend = new VaultBackend(serverBin(), home)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  async function runBlock(page: Page, index: number): Promise<void> {
    const marker = `transcript-${String(index).padStart(4, '0')}`
    const command = `printf '${marker}-%03d\\n' {1..${ROWS_PER_BLOCK}}`
    const input = page.locator(INPUT)

    await input.fill(command)
    await page.keyboard.press('Enter')

    // Positional, not `hasText`: a text filter re-scans every already-frozen
    // block's content on every poll. Blocks freeze in order and are never
    // removed, so the index-th command is the index-th frozen block.
    //
    // ONE IN-PAGE PREDICATE, NOT THREE LOCATOR ASSERTIONS (nocx-2v80t.3.35).
    // Every poll of `expect(locator).toBeVisible()` that does not match yet —
    // most of them, while the command runs — makes Playwright render an ARIA
    // snapshot of the whole body for its error message, and its selector
    // engine resolves `nth()` by walking the document: both are O(document)
    // per poll, and together they grew each command's cost with the
    // transcript. The same three facts — the index-th frozen block is visible,
    // it holds its command's first row, and no block is running — are read
    // natively here, in the page, once per frame.
    await page
      .waitForFunction(
        ({ frozen, running, n, first }) => {
          const block = document.querySelectorAll<HTMLElement>(frozen)[n - 1]
          return (
            block !== undefined &&
            block.checkVisibility() &&
            (block.textContent ?? '').includes(first) &&
            document.querySelector(running) === null
          )
        },
        { frozen: BLOCK, running: RUNNING_BLOCK, n: index, first: `${marker}-001` },
        { timeout: 60_000 },
      )
      .catch((error: unknown) => {
        throw new Error(`the command ${marker} never froze as block ${index}: ${String(error)}`)
      })
  }
  async function measure(page: Page): Promise<TranscriptMetrics> {
    return page.evaluate(
      async ({ blockSelector, rowSelector, scrollerSelector, frameSamples, idleSamples }) => {
        const scroller = document.querySelector<HTMLElement>(scrollerSelector)
        if (!scroller) throw new Error(`missing transcript scroller: ${scrollerSelector}`)

        const maxScrollTop = Math.max(0, scroller.scrollHeight - scroller.clientHeight)
        scroller.scrollTop = 0

        // The display at rest: the same page, the same position, nothing
        // written between frames.
        const idleFrames: number[] = []
        await new Promise<void>((resolve) => {
          let last: number | null = null
          const tick = (timestamp: number): void => {
            if (last !== null) idleFrames.push(timestamp - last)
            last = timestamp
            if (idleFrames.length === idleSamples) {
              resolve()
              return
            }
            requestAnimationFrame(tick)
          }
          requestAnimationFrame(tick)
        })

        const frameTimes: number[] = []
        let previousTimestamp: number | null = null
        let frame = 0
        await new Promise<void>((resolve) => {
          const sample = (timestamp: number): void => {
            if (previousTimestamp !== null) frameTimes.push(timestamp - previousTimestamp)
            previousTimestamp = timestamp
            scroller.scrollTop = maxScrollTop * (frame / Math.max(1, frameSamples - 1))
            frame += 1
            if (frame === frameSamples) {
              requestAnimationFrame(() => resolve())
              return
            }
            requestAnimationFrame(sample)
          }
          requestAnimationFrame(sample)
        })

        return {
          blocks: document.querySelectorAll(blockSelector).length,
          rows: document.querySelectorAll(rowSelector).length,
          domNodes: scroller.querySelectorAll('*').length,
          scrollHeight: scroller.scrollHeight,
          clientHeight: scroller.clientHeight,
          maxScrollTop,
          idleFrames,
          scrollFrames: frameTimes,
        }
      },
      {
        blockSelector: BLOCK,
        rowSelector: ROW,
        scrollerSelector: SCROLLER,
        frameSamples: FRAME_SAMPLES,
        idleSamples: IDLE_SAMPLES,
      },
    )
  }

  async function waitForFrame(page: Page): Promise<void> {
    await page.evaluate(
      () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())),
    )
  }

  async function blockInViewport(page: Page, marker: string): Promise<boolean> {
    return page.evaluate(
      ({ marker: text, scrollerSelector }) => {
        const scroller = document.querySelector<HTMLElement>(scrollerSelector)
        const row = [...document.querySelectorAll<HTMLElement>('.term-grid-row')].find((element) =>
          element.textContent?.includes(text),
        )
        if (!scroller || !row) throw new Error(`missing marker ${text}`)
        const viewport = scroller.getBoundingClientRect()
        const target = row.getBoundingClientRect()
        return target.bottom > viewport.top && target.top < viewport.bottom
      },
      { marker, scrollerSelector: SCROLLER },
    )
  }

  async function selectionEvidence(page: Page): Promise<SelectionEvidence> {
    await page.evaluate((scrollerSelector) => {
      const scroller = document.querySelector<HTMLElement>(scrollerSelector)
      if (!scroller) throw new Error(`missing transcript scroller: ${scrollerSelector}`)
      scroller.scrollTop = 0
    }, SCROLLER)
    await waitForFrame(page)

    const visibleBlockInViewport = await blockInViewport(page, 'transcript-0001-001')
    const visibleBlockText = await page.evaluate(() => {
      const row = [...document.querySelectorAll<HTMLElement>('.term-grid-row')].find((element) =>
        element.textContent?.includes('transcript-0001-001'),
      )
      if (!row) throw new Error('missing visible marker')
      const selection = window.getSelection()
      if (!selection) throw new Error('the browser exposed no native selection')
      const range = document.createRange()
      if (!row.firstChild) throw new Error('visible marker has no text node')
      range.selectNodeContents(row)
      selection.removeAllRanges()
      selection.addRange(range)
      return selection.toString()
    })

    const crossBlock = await page.evaluate((blockSelector) => {
      const blocks = [...document.querySelectorAll<HTMLElement>(blockSelector)]
      const first = blocks[0]?.querySelector<HTMLElement>('.term-grid-row')
      const second = blocks[1]?.querySelector<HTMLElement>('.term-grid-row:last-child')
      if (!first || !second || !first.firstChild || !second.firstChild) {
        throw new Error('the first two transcript blocks have no selectable rows')
      }
      const selection = window.getSelection()
      if (!selection) throw new Error('the browser exposed no native selection')
      const range = document.createRange()
      range.setStart(first.firstChild, 0)
      range.setEnd(second.firstChild, second.textContent?.length ?? 0)
      selection.removeAllRanges()
      selection.addRange(range)
      return selection.toString()
    }, BLOCK)

    return {
      crossBlockTextLength: crossBlock.length,
      crossBlockIncludesFirst: crossBlock.includes('transcript-0001-001'),
      crossBlockIncludesSecond: crossBlock.includes('transcript-0002-100'),
      visibleBlockText,
      visibleBlockInViewport,
    }
  }

  async function findEvidence(page: Page): Promise<FindEvidence> {
    const visibleBeforeSearch = await blockInViewport(page, 'transcript-0001-001')
    // The native find starts where the SELECTION is and does not wrap, and this
    // spec has just selected across two blocks: without clearing it, a search
    // for a row ABOVE the selection (the first block's) is reported as not found
    // while the row is on screen, which is the browser's starting point and not
    // a missing capability.
    await page.evaluate(() => window.getSelection()?.removeAllRanges())
    const visibleFound = await page.evaluate(() => {
      const find = (window as Window & { find?: (text: string) => boolean }).find
      if (typeof find !== 'function') throw new Error('the browser exposed no native find')
      return find.call(window, 'transcript-0001-001')
    })

    await page.evaluate((scrollerSelector) => {
      const scroller = document.querySelector<HTMLElement>(scrollerSelector)
      if (!scroller) throw new Error(`missing transcript scroller: ${scrollerSelector}`)
      scroller.scrollTop = 0
    }, SCROLLER)
    await waitForFrame(page)
    const offscreenBeforeSearch = !(await blockInViewport(page, LAST_MARKER))
    const offscreenFound = await page.evaluate((marker: string) => {
      const find = (window as Window & { find?: (text: string) => boolean }).find
      if (typeof find !== 'function') throw new Error('the browser exposed no native find')
      return find.call(window, marker)
    }, LAST_MARKER)

    return {
      visibleFound,
      visibleBeforeSearch,
      offscreenFound,
      offscreenBeforeSearch,
    }
  }

  /**
   * A BLOCK'S STORED ROWS ARE ITS OWN COMMAND'S, IN ORDER (nocx-2v80t.3.9).
   *
   * The painted row count is not the criterion, and a bound on it is all it can
   * honestly be: a block paints its interval's departed rows PLUS the boundary
   * screen the close appends, so a hundred-line command's block paints 102 rows
   * — the command's echo (which the block's header already shows) and the
   * boundary screen's blank row included. Measured: 102 for every block, 103 for
   * the first, whose screen also carries the pane's opening prompt, so 100..106
   * is the band this shape produces per block and a regression that appends
   * another command's rows (26 to 29 of them, measured before the fix) lands
   * outside it.
   *
   * What the bead demands is attribution, and it is exact in two places: the
   * STORE, read through the control plane (ledger.get -> application/x-nocx-rows
   * -> ledger.artifact), and the DOM's own markers. Every block must hold exactly
   * its own command's hundred numbered rows, in order, and no numbered row of any
   * other command.
   */
  const PAINTED_SLACK = 6

  async function everyBlockRows(page: Page, endpoint: { port: number; token: string }) {
    const ids = await page
      .locator(BLOCK)
      .evaluateAll((blocks) => blocks.map((block) => (block as HTMLElement).dataset.entryId ?? ''))
    const dom = await page.locator(BLOCK).evaluateAll((blocks) =>
      blocks.map((block, index) => ({
        own: `transcript-${String(index + 1).padStart(4, '0')}-`,
        texts: [...block.querySelectorAll('.cmd-output .term-grid-row')].map((row) =>
          (row.textContent ?? '').replace(/\s+$/, ''),
        ),
      })),
    )
    const wire = await openControlPlane(endpoint.port, endpoint.token)
    const stored: string[][] = []
    try {
      for (const id of ids) {
        const detail = (await wire.call('ledger.get', { id })) as {
          artifacts: Array<{ mediaType: string; id: string }>
        }
        const rowsArtifact = detail.artifacts.find(
          (artifact) => artifact.mediaType === 'application/x-nocx-rows',
        )
        if (!rowsArtifact) {
          stored.push([])
          continue
        }
        const body = (await wire.call('ledger.artifact', { id: rowsArtifact.id })) as {
          body: string
        }
        // A stored row carries its line as `text` directly (nocx-zg3k3.2.12,
        // internal/content/block_rows_encode.go's `blockRow`); the per-column
        // `cells` shape this used to rejoin was retired by that change, as
        // ws_block_rows_test.go's own read side was updated in the same run
        // (14ad5e94) to read `row.text` rather than `row.cells`.
        stored.push(
          body.body
            .split('\n')
            .filter(Boolean)
            .map((line) =>
              (JSON.parse(line) as { row: { text: string } }).row.text.replace(/\s+$/, ''),
            ),
        )
      }
    } finally {
      wire.close()
    }
    return { dom, stored }
  }

  /** One block's numbered rows: the markers, never the command echo. */
  function numbered(rows: string[], own: string) {
    const markers = rows.filter((row) => /^transcript-\d{4}-/.test(row))
    const mine = markers.filter((row) => row.startsWith(own))
    const foreign = markers.filter((row) => !row.startsWith(own))
    return {
      mine: mine.length,
      foreign,
      ordered:
        mine.length === ROWS_PER_BLOCK &&
        mine.every((row, i) => row === `${own}${String(i + 1).padStart(3, '0')}`),
      painted: rows.length,
    }
  }

  test('measures 500 blocks and preserves native selection/search', async ({ page }) => {
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await appReadyForInput(page)

    for (let index = 1; index <= BLOCKS; index += 1) await runBlock(page, index)

    await expect(page.locator(BLOCK)).toHaveCount(BLOCKS, { timeout: 60_000 })

    // THE CRITERION: every block frozen, and every block holding exactly its own
    // command's hundred rows — in the STORE, and in what the DOM paints.
    const { dom, stored } = await everyBlockRows(page, endpoint)
    let painted = 0
    for (let i = 0; i < dom.length; i += 1) {
      const own = dom[i].own
      const inStore = numbered(stored[i], own)
      expect(inStore.mine, `block ${i + 1} holds ${inStore.mine} of its own stored rows`).toBe(
        ROWS_PER_BLOCK,
      )
      expect(inStore.foreign, `block ${i + 1} holds another command's stored rows`).toEqual([])
      expect(inStore.ordered, `block ${i + 1} holds its stored rows out of order`).toBe(true)

      const inDom = numbered(dom[i].texts, own)
      // The marker checks above see only rows matching transcript-NNNN-: an
      // extra or missing STORED row that carries no marker of its own —
      // duplicated or garbled ahead of a real column-count defect, say — is
      // invisible to them. The DOM paints every stored row 1:1
      // (paintStoredRows appends one .term-grid-row per line in
      // stored.lines) and adds no row of its own — the incomplete notice is
      // a sibling div, not a .term-grid-row — so the two counts must match
      // exactly, not just agree on the numbered subset.
      expect(
        dom[i].texts.length,
        `block ${i + 1} paints ${dom[i].texts.length} rows for ${stored[i].length} stored`,
      ).toBe(stored[i].length)
      expect(inDom.mine, `block ${i + 1} paints ${inDom.mine} of its own rows`).toBe(ROWS_PER_BLOCK)
      expect(inDom.ordered, `block ${i + 1} paints its rows out of order`).toBe(true)
      painted += inDom.painted
    }
    expect(painted).toBeGreaterThanOrEqual(BLOCKS * ROWS_PER_BLOCK)
    expect(painted).toBeLessThanOrEqual(BLOCKS * (ROWS_PER_BLOCK + PAINTED_SLACK))

    const selection = await selectionEvidence(page)
    const find = await findEvidence(page)
    const { idleFrames, scrollFrames, ...metrics } = await measure(page)
    const frames: FrameVerdict = judgeFrames(idleFrames, scrollFrames)

    console.log(
      `TRANSCRIPT_METRICS ${JSON.stringify({ ...metrics, idleSamples: idleFrames.length, scrollSamples: scrollFrames.length, ...frames })}`,
    )
    console.log(`TRANSCRIPT_SELECTION ${JSON.stringify(selection)}`)
    console.log(`TRANSCRIPT_NATIVE_FIND ${JSON.stringify(find)}`)

    expect(selection.visibleBlockInViewport).toBe(true)
    expect(selection.visibleBlockText).toContain('transcript-0001-001')
    expect(selection.crossBlockIncludesFirst).toBe(true)
    expect(selection.crossBlockIncludesSecond).toBe(true)
    expect(find.visibleBeforeSearch).toBe(true)
    expect(find.visibleFound).toBe(true)
    expect(find.offscreenBeforeSearch).toBe(true)
    expect(find.offscreenFound).toBe(true)

    expect(frames.failures, 'the transcript scroll missed frames').toEqual([])
  })
})
