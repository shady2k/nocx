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
 *
 * Selection and find are asserted; the frame times are printed
 * (TRANSCRIPT_METRICS) and annotated on the report, never asserted — a
 * duration depends on the machine (see the end of the test).
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
import { recordRowsCensusForTest } from './failure-context'

const serverBin = () => readStand().server

const BLOCKS = 500
const ROWS_PER_BLOCK = 100
// The zero-retention subtest's single command must output far more rows than
// the library's bounded capture floor at 0 lines (~399 rows at 80 cols, design
// section 7): with a small batch the pages are never pruned and the test would
// pass without the history-erased effect. 5000 rows force pruning inside the
// write, which is the mechanism under test.
const ZERO_COMMAND_ROWS = 5000
// The ledger search limit the zero subtest queries with; must hold the whole
// command's rows after restart.
const ZERO_QUERY_LIMIT = 6000
// The transcript's own last row: the off-screen half of the native find probe.
// Derived from BLOCKS, never spelled out — a reduced run is the same spec.
const LAST_MARKER = `transcript-${String(BLOCKS).padStart(4, '0')}-${String(ROWS_PER_BLOCK).padStart(3, '0')}`
const FRAME_SAMPLES = 180
// Idle frames sampled just before the scroll, in the same page, with nothing
// moving: the display interval the scroll is reported against. Why it cannot come
// from the scroll itself, and the criterion, are in e2e/frame-budget.mts.
const IDLE_SAMPLES = 120
const INPUT = '.pane.active .nocx-editor-input'
const BLOCK = '.pane.active .scrollback-inner > .cmd-block:not(.cmd-block-running)'
const RUNNING_BLOCK = '.pane.active .scrollback-inner > .cmd-block.cmd-block-running'
const SCROLLER = '.pane.active .scrollback-area'
const ROW = `${BLOCK} .cmd-output .term-grid-row`
// The media type a command's streamed rows are stored under (contracts/ledger.blockRows.schema.json).
const ROWS_MEDIA_TYPE = 'application/x-nocx-rows'
// How much of the backend's log the census greps for the seal's own numbers.
// It is read at the failure, while the file is live; the appends of a
// 5000-row command are a small part of a few hundred kilobytes.
const CENSUS_LOG_BYTES = 8 * 1024 * 1024

/** One artifact's metadata, as ledger.get reports it (contracts/ledger.get.schema.json). */
interface LedgerArtifactMeta {
  id: string
  executionId: number
  mediaType: string
  state: string
  byteLen: number
  chunkCount: number
  truncated: string | null
  payload: unknown
}

/**
 * WHAT THE STORE ACTUALLY HOLDS FOR ONE COMMAND (nocx-rb4ca), taken at the
 * moment this spec's own comparison of the stored rows fails.
 *
 * A short durable transcript has three shapes that look identical in the
 * failure report, and the backend log alone cannot tell them apart: the rows
 * arrived and were refused into a sealed artifact, they are parked in a SECOND
 * rows artifact of the same entry (a reader takes the first one —
 * internal/transport/ws_blocks.go's blockBody returns one artifact and never
 * joins a second), or they never arrived at all. So this reads back everything
 * the read path can see, in the read path's own terms:
 *
 *   - every artifact of the entry whose media type is application/x-nocx-rows,
 *     with the state and payload the store reports for it;
 *   - for each, the rows READ THROUGH ledger.artifact — how many, how many
 *     carry this command's marker, and the absolute row RANGE (the `from` of
 *     its first and last line, which is the index space the seal's own cursor
 *     and endRow are stated in);
 *   - and the store's and the transport's own lines about the seal, taken
 *     verbatim from the backend log: `block rows close: sealing` (its cursor,
 *     dropped, lost, unavailable) and `interval end sealed the block` (the
 *     interval end's endRow). Those two numbers are what the report has never
 *     carried, and they are the ones that say whether the artifact's own
 *     record agrees with the read.
 *
 * Read-only: it asks the same questions findStoredRows asks, by the same
 * methods, and changes nothing about the comparison it explains.
 */
async function rowsCensus(
  ep: { port: number; token: string },
  backendLog: string,
  command: string,
  marker: string,
): Promise<string> {
  const out: string[] = [`command ${JSON.stringify(command)}`]
  const wire = await openControlPlane(ep.port, ep.token)
  try {
    const query = (await wire.call('ledger.query', {
      scope: 'everywhere',
      limit: ZERO_QUERY_LIMIT,
    })) as { entries: Array<{ id: string; intent: string }> }
    const entries = query.entries.filter((entry) => entry.intent === command)
    out.push(
      `ledger.query: ${query.entries.length} entries in the page, ${entries.length} of them this command`,
    )
    for (const entry of entries) {
      const detail = (await wire.call('ledger.get', { id: entry.id })) as {
        artifacts: LedgerArtifactMeta[]
      }
      const rowsArtifacts = detail.artifacts.filter(
        (artifact) => artifact.mediaType === ROWS_MEDIA_TYPE,
      )
      // WHICH ONE THE READER TAKES is the question a list of artifacts cannot
      // answer on its own: both this spec's own read and the product's
      // blockBody take the FIRST artifact of this media type in execution
      // order and never join a second (internal/transport/ws_blocks.go), so
      // the first line below is the one the comparison actually saw.
      out.push(
        `entry ${entry.id}: ${detail.artifacts.length} artifact(s), ` +
          `${rowsArtifacts.length} of media type ${ROWS_MEDIA_TYPE}`,
      )
      out.push(
        rowsArtifacts.length === 0
          ? '  the read path takes: nothing — no artifact of this media type exists for this entry'
          : `  the read path takes the first of these: ${rowsArtifacts[0]!.id}`,
      )
      for (const [index, artifact] of rowsArtifacts.entries()) {
        const body = (await wire.call('ledger.artifact', { id: artifact.id })) as { body: string }
        const rows = body.body
          .split('\n')
          .filter(Boolean)
          .map((line) => JSON.parse(line) as { from: number; row: { text: string } })
        const mine = rows.filter((row) => row.row.text.replace(/\s+$/, '').startsWith(`${marker}-`))
        const first = rows[0]
        const last = rows[rows.length - 1]
        const text = (row: { row: { text: string } } | undefined) =>
          row ? JSON.stringify(row.row.text.replace(/\s+$/, '')) : '(none)'
        out.push(
          `  artifact ${index === 0 ? '(the read takes this one)' : '(not read)'} ` +
            `${artifact.id} execution=${artifact.executionId} ` +
            `state=${artifact.state} byteLen=${artifact.byteLen} ` +
            `chunks=${artifact.chunkCount} truncated=${String(artifact.truncated)} ` +
            `payload=${JSON.stringify(artifact.payload)}`,
        )
        out.push(
          `    rows ${rows.length}, marker rows ${mine.length}, ` +
            `range [${first ? first.from : '-'}..${last ? last.from : '-'}], ` +
            `first ${text(first)}, last ${text(last)}`,
        )
      }
      // The two shapes a short read can have, stated rather than left to be
      // worked out: a tail parked in another artifact is invisible to a reader
      // that joins none of them, and an artifact that is the only one rules
      // that shape out entirely.
      out.push(
        rowsArtifacts.length === 1
          ? '  one rows artifact exists: a missing tail cannot be parked in a second one — it is this artifact or it never arrived'
          : `  ${rowsArtifacts.length} rows artifacts exist and the read joins none of them: only the first is read, so a tail in any of the others is invisible to this comparison`,
      )
    }
    if (entries.length === 0) {
      out.push('(no entry in this page has this command as its intent)')
    }
  } finally {
    wire.close()
  }
  const seals = backendLog
    .split('\n')
    .filter(
      (line) =>
        line.includes('block rows close: sealing') ||
        line.includes('interval end sealed the block'),
    )
  out.push(
    seals.length === 0
      ? 'backend log: neither "block rows close: sealing" nor "interval end sealed the block" is in it'
      : `backend log, the store's and the transport's own lines:\n    ${seals.join('\n    ')}`,
  )
  return out.join('\n')
}

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

  // Active stage acceptance for nocx-zg3k3.5: durable capture must retain
  // every row independently of the default live scrollback budget.

  test('measures 500 blocks and preserves native selection/search', async ({ page }) => {
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await appReadyForInput(page)

    for (let index = 1; index <= BLOCKS; index += 1) await runBlock(page, index)

    await expect(page.locator(BLOCK)).toHaveCount(BLOCKS, { timeout: 60_000 })
    // `runBlock` observes the frozen card and its first row, not completion of
    // the separate ledger.artifact fetch that paints stored rows. block.closed
    // starts that fetch before the card freezes, but the notification does not
    // await it; the last card can therefore be visible with only a prefix
    // painted. Before comparing the consumer DOM to the canonical store, wait
    // for the final stored row of every block to mount. This is observable
    // readiness, not a delay or a substitute for the exact count assertions
    // below.
    await page.waitForFunction(
      ({ selector, count, rowsPerBlock }) => {
        const blocks = document.querySelectorAll<HTMLElement>(selector)
        if (blocks.length !== count) return false
        return [...blocks].every((block, index) => {
          const rows = block.querySelectorAll('.cmd-output .term-grid-row')
          const marker =
            `transcript-${String(index + 1).padStart(4, '0')}-` +
            String(rowsPerBlock).padStart(3, '0')
          return (
            rows.length === rowsPerBlock &&
            (rows[rows.length - 1]?.textContent ?? '').includes(marker)
          )
        })
      },
      { selector: BLOCK, count: BLOCKS, rowsPerBlock: ROWS_PER_BLOCK },
      { timeout: 60_000 },
    )

    // THE CRITERION: every block frozen, and every block holding exactly its own
    // command's hundred rows — in the STORE, and in what the DOM paints.
    const { dom, stored } = await everyBlockRows(page, endpoint)
    let painted = 0
    for (let i = 0; i < dom.length; i += 1) {
      const own = dom[i].own
      // NO OUTPUT ON THE COMMAND'S OWN LINE (nocx-2v80t.3.46). A resize that
      // lands at the submit makes bash redraw its line with no newline, and
      // the command's first output row then continues on it — a stored row
      // that is the command line AND a numbered row. Named before the counts
      // below, which would only report that one row went missing.
      const joined = stored[i].filter((row) => /printf .*transcript-\d{4}-\d{3}/.test(row))
      expect(joined, `block ${i + 1} carries its command line in an output row`).toEqual([])
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

    // THE FRAME TIMES ARE REPORTED, NOT ASSERTED (the owner's decision of
    // 2026-09-28, nocx-zg3k3.2.10). Over 8 CI runs this transcript scrolled at
    // a 16.7 ms median every time and a p95 of 33.4 ms in 7 and 50 ms in 1: it
    // drops a frame in its worst twentieth on CI's runner, and the one red run
    // was that runner being slower that day. A gate on a duration is a gate on
    // the machine (AGENTS.md: a test may not depend on timing). What makes the
    // scroll budgeted is bounding the page to what is visible, which is
    // nocx-zg3k3.11's, and it asserts a node count. Here the measurement must
    // only have run, over the whole transcript, so the numbers above are real.
    expect(idleFrames).toHaveLength(IDLE_SAMPLES)
    expect(scrollFrames).toHaveLength(FRAME_SAMPLES - 1)
    expect(metrics.maxScrollTop).toBeGreaterThan(metrics.clientHeight)
    test.info().annotations.push({
      type: 'frame budget (reported, not asserted)',
      description: `median ${frames.medianMs} ms, p95 ${frames.p95Ms} ms against an idle ${frames.baselineMs} ms; ${frames.failures.join('; ') || 'within budget'}`,
    })
  })

  // WAS SKIPPED 2026-10-05, UN-SKIPPED WITH THE FIX (nocx-n5ent). After a
  // coordinator restart the stored transcript lost the tail of the command: one
  // sealed artifact, `truncated=gap`, `endRow == cursor` (a seal with no closing
  // screen) and 1157 rows never stored, because an interval was frozen at the
  // count measured on the authenticated channel while the pty still held the
  // command's output. The settle that could do that is deferred now:
  // a completion for another nonce, an environment entry and the rendezvous
  // bound no longer freeze an interval whose rows are still in flight — the
  // interval keeps them and is sealed by its own fence's sighting, or by the
  // byte stream's next boundary when that fence truly never comes
  // (internal/sessionruntime/observation.go, deferPendingLocked; ADR-0074
  // case 3 amended).
  test('zero live retention leaves durable output after restart and no live history', async ({
    page,
  }, testInfo) => {
    const endpoint = await backend.start()
    const setupWire = await openControlPlane(endpoint.port, endpoint.token)
    try {
      // Set through the same settings RPC the Settings screen uses, before
      // the initial pane is created so its spawn carries the zero budget.
      await setupWire.call('settings.set', { key: 'terminal.scrollbackLines', value: 0 })
      // A single 5000-row command is past the default per-command cap (256 KiB,
      // ~80 bytes per encoded row), so raise it to its maximum: this test is
      // about the zero-budget capture floor, not the output cap.
      await setupWire.call('settings.set', { key: 'history.outputCapKB', value: 4096 })
    } finally {
      setupWire.close()
    }
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await appReadyForInput(page)

    const marker = 'transcript-zero-retention'
    // One command far larger than the zero-budget capture floor, so pruning
    // inside the write is exercised (see ZERO_COMMAND_ROWS).
    const command = `printf '${marker}-%03d\\n' {1..${ZERO_COMMAND_ROWS}}`
    await page.locator(INPUT).fill(command)
    await page.keyboard.press('Enter')
    await expect(page.locator(BLOCK)).toHaveCount(1, { timeout: 30_000 })

    const findStoredRows = async (ep: typeof endpoint): Promise<string[]> => {
      const wire = await openControlPlane(ep.port, ep.token)
      try {
        const query = (await wire.call('ledger.query', {
          scope: 'everywhere',
          limit: ZERO_QUERY_LIMIT,
        })) as { entries: Array<{ id: string; intent: string }> }
        const entry = query.entries.find((candidate) => candidate.intent === command)
        if (!entry) return []
        const detail = (await wire.call('ledger.get', { id: entry.id })) as {
          artifacts: Array<{ mediaType: string; id: string }>
        }
        const rowsArtifact = detail.artifacts.find(
          (artifact) => artifact.mediaType === 'application/x-nocx-rows',
        )
        if (!rowsArtifact) return []
        const body = (await wire.call('ledger.artifact', { id: rowsArtifact.id })) as {
          body: string
        }
        return (
          body.body
            .split('\n')
            .filter(Boolean)
            .map((line) =>
              (JSON.parse(line) as { row: { text: string } }).row.text.replace(/\s+$/, ''),
            )
            // Drop the echoed command line (the terminal's own echo of the
            // input) and keep only the marker rows this command generated.
            .filter((text) => text.startsWith(`${marker}-`))
        )
      } finally {
        wire.close()
      }
    }

    const own = Array.from(
      { length: ZERO_COMMAND_ROWS },
      (_, index) => `${marker}-${String(index + 1).padStart(3, '0')}`,
    )

    // The census is taken ONLY on the path where this comparison fails, and it
    // is taken HERE rather than in the failure report because the backend this
    // read needs is stopped by `afterEach` before that report runs (nocx-rb4ca).
    // It never throws: a diagnostic that is missing must not replace the
    // failure it was taken for, so a read that fails is recorded as the line
    // saying so.
    const recordCensus = async (label: string, ep: { port: number; token: string }) => {
      try {
        recordRowsCensusForTest(
          testInfo.testId,
          label,
          await rowsCensus(ep, backend.logTail(CENSUS_LOG_BYTES), command, marker),
        )
      } catch (censusError) {
        recordRowsCensusForTest(
          testInfo.testId,
          label,
          `(the store could not be read for this census: ${String(censusError)})`,
        )
      }
    }

    try {
      await expect.poll(async () => findStoredRows(endpoint), { timeout: 30_000 }).toEqual(own)
    } catch (error) {
      await recordCensus('the comparison before the restart', endpoint)
      throw error
    }

    const restarted = await backend.restart()
    await bindEndpoint(page, restarted)
    await page.reload()
    await appReadyForInput(page)
    await expect(page.locator(BLOCK)).toHaveCount(1, { timeout: 30_000 })
    const storedAfterRestart = await findStoredRows(restarted)
    try {
      expect(storedAfterRestart).toEqual(own)
    } catch (error) {
      await recordCensus('the comparison after the restart', restarted)
      throw error
    }

    const pane = page.locator('.pane.active')
    const sessionId = await pane.getAttribute('data-session-id')
    expect(sessionId).toBeTruthy()
    const historyWire = await openControlPlane(restarted.port, restarted.token)
    try {
      const pageResult = (await historyWire.call('session.historyPage', {
        sessionId,
        before: null,
        limit: 64,
      })) as { start: number; end: number; floor: number }
      expect(pageResult.end - pageResult.start, 'zero-budget live history page is empty').toBe(0)
    } finally {
      historyWire.close()
    }
    await expect(page.locator('.pane.active .live-history-page')).toHaveCount(0)
  })
})
