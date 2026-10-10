/**
 * When a test fails, print everything it takes to read WHY — right there in
 * the reporter output, not behind a CI artifact download (nocx-n14oo.11).
 *
 * The owner's complaint (2026-09-16) was concrete: debugging a red e2e test
 * meant downloading CI artifacts and reading error-context.md plus a backend
 * log that belonged to a DIFFERENT spec (nocx-ky68q). Both halves are fixed
 * here — this module gathers and prints the block; nocx-ky68q's half (each
 * backend's preserved log landing under its own test) is in harness.ts's
 * VaultBackend.preserveLog.
 *
 * Five sources, each bounded so a chatty test cannot bury the one line that
 * mattered:
 *
 *   - The shared stand's backend log, filtered to this test's own trace_id
 *     (e2e/trace-context.ts) — the stand outlives every test, so nothing but
 *     the filter tells one test's lines from the next test's.
 *   - Any backend a spec started for ITSELF (VaultBackend), registered here
 *     by registerBackendForTest and printed in full: unlike the shared
 *     stand, one of these belongs to exactly one test for its whole life, so
 *     there is nothing to filter.
 *   - The browser's console messages and uncaught page errors — of the
 *     fixture's page, and of every page a spec built itself and put on the
 *     report with watchPageForTest.
 *   - The control-plane's own JSON-RPC frames — method, id and error ONLY.
 *   - The block DOM (nocx-itmo2) — every `.cmd-block` on the page, its parent
 *     chain, and the counts a `.pane.active .scrollback-inner > .cmd-block`
 *     selector walks through. Read ONLY on failure, and it is the one thing the
 *     accessibility snapshot cannot settle: a run failed on exactly that
 *     selector while the snapshot showed the block settled on screen, so what
 *     is missing is the DOM ownership the flattened snapshot hides.
 *   - The ledger's own record of the rows a spec compared (nocx-rb4ca) —
 *     recorded, not read here: a spec asks the store its question the moment
 *     its OWN comparison fails (recordRowsCensusForTest) and the report prints
 *     what it got. It has to be that way round because a spec's backend is
 *     stopped by `afterEach` before this report runs, so a census asked for
 *     here would be a read of a socket that is already gone. It exists to
 *     answer the one question the backend log cannot: whether a short stored
 *     transcript is rows that never arrived, rows parked in a second artifact,
 *     or rows the artifact's own record says were never in it.
 *
 * WHO PRINTS IT. The report is written by an AUTO fixture (harness.ts), once
 * per test, whichever fixtures the test asked for. It used to be written by
 * the `page` fixture, and a spec that never asked for `page` — every spec that
 * builds its own client from `browser`, because it needs a fresh context per
 * coordinator — printed nothing at all when it failed: not its console, and
 * not even the backend it had registered here (remote-coordinator-reclaim,
 * CI 2026-09-19, a hidden editor and no block to say why).
 *
 * What is deliberately NEVER printed: a frame's params or result (a
 * password, an API key, the text of a command someone typed — see
 * redact-frame.ts), and the data plane (raw PTY bytes; AD-1 keeps it off the
 * control-plane frames this module listens for in the first place, so there
 * is nothing here to filter it OUT of — it was never IN).
 */
import type { Page, TestInfo } from '@playwright/test'

import { formatFrame, redactFrame, type RedactedFrame } from './redact-frame.mts'
import { standBackendLogTail } from './stand'
import { linesForTrace } from './trace-context.mts'

/** The shape VaultBackend already has; declared structurally so this module
 *  never has to import harness.ts (which imports THIS module, for the page
 *  fixture) — that would be a cycle for a single getter and one method. */
export interface RegisterableBackend {
  logFile: string
  logTail(maxBytes?: number): string
  goroutineDump?: string
}

const backendsByTestId = new Map<string, RegisterableBackend[]>()

/** One checkpoint's census, as its spec recorded it: what the ledger held for
 *  the rows the spec was comparing, at the moment its own comparison failed. */
export interface RecordedRowsCensus {
  /** What the spec was checking when it took this — the checkpoint's own
   *  words, so a test with two comparisons does not read as one. */
  label: string
  /** The census, already rendered: a spec owns the reading (it knows the
   *  command and the endpoint) and this module owns only the printing. */
  text: string
}

const rowsCensusByTestId = new Map<string, RecordedRowsCensus[]>()

/**
 * Record what the ledger held for one spec's own comparison, for the failure
 * report to print (nocx-rb4ca).
 *
 * WHY IT IS RECORDED AND NOT READ BY THE REPORT. A spec's backend is stopped
 * by its `afterEach`, which runs before the report's auto fixture, so anything
 * this module tried to ask the store at report time would be a read of a
 * socket that no longer exists — and the interesting moment is the failure
 * itself, not a minute later. The spec therefore takes the census while the
 * store is still up, on the only path that needs it (its own failing
 * comparison), and the report prints it verbatim.
 *
 * Appended, never replaced: one test can compare the same entry at more than
 * one checkpoint (before and after a restart), and the difference between the
 * two is often the whole answer.
 */
export function recordRowsCensusForTest(testId: string, label: string, text: string): void {
  const list = rowsCensusByTestId.get(testId) ?? []
  list.push({ label, text })
  rowsCensusByTestId.set(testId, list)
}

/** Attribute a backend a spec started for itself to the test running right
 *  now, so a failure's printed block finds it without the spec having to say
 *  anything. Idempotent per (testId, backend) pair — a restart calls this
 *  again with the same instance. */
export function registerBackendForTest(testId: string, backend: RegisterableBackend): void {
  const list = backendsByTestId.get(testId) ?? []
  if (!list.includes(backend)) list.push(backend)
  backendsByTestId.set(testId, list)
}

const MAX_CONSOLE_LINES = 50
const MAX_FRAMES = 50
const MAX_BACKEND_LINES = 200

/** A fixed-capacity FIFO that counts what it dropped, so a report can say
 *  "N earlier dropped" instead of silently truncating with no sign anything
 *  is missing. */
function bounded<T>(max: number): { push(item: T): void; items: T[]; dropped: number } {
  const items: T[] = []
  let dropped = 0
  return {
    push(item: T) {
      items.push(item)
      if (items.length > max) {
        items.shift()
        dropped++
      }
    },
    items,
    get dropped() {
      return dropped
    },
  }
}

/** What one watched page collected, and the one moment its accessibility
 *  snapshot and its block DOM can still be taken. */
export interface PageDiagnostics {
  /** Read the page's accessibility tree NOW, while it is still open, and keep
   *  it for the report. A fixture page is closed by the time the auto fixture
   *  reports, so the `page` fixture calls this on its own way out; a closed
   *  page keeps whatever was taken last. */
  captureSnapshot(): Promise<void>
  /** Whether a snapshot has been taken already. */
  hasSnapshot(): boolean
  /** Read the page's `.cmd-block` DOM NOW, for the same reason and at the same
   *  moment as `captureSnapshot` — and, like it, only on a way out that is
   *  already known to be a failure. A page a SPEC owns is usually still open
   *  when the report is written, and is read then instead. Never throws: a
   *  page that has gone away is answered with a line saying so. */
  captureBlockDom(): Promise<void>
  /** Whether the block DOM has been read already. */
  hasBlockDom(): boolean
  /** This page's sections of the printed block. */
  sections(label: string): string[]
}

interface WatchedPage {
  label: string
  page: Page
  diagnostics: PageDiagnostics
  /** The harness closes this page's context after reporting — true for a
   *  page a spec built itself from `browser`, which no fixture owns. */
  closeAfterReport: boolean
}

const pagesByTestId = new Map<string, WatchedPage[]>()

/**
 * Start listening on `page` for everything this module can print, and
 * return what it collects.
 *
 * Listeners are attached immediately — before navigation — so nothing the
 * page does before its first assertion is missed. Collecting is unconditional
 * (a passing test costs a few event-listener calls); only the report decides
 * whether anything is PRINTED.
 */
export function attachFailureDiagnostics(page: Page): PageDiagnostics {
  const consoleLines = bounded<string>(MAX_CONSOLE_LINES)
  const frames = bounded<RedactedFrame>(MAX_FRAMES)
  let snapshot: string | null = null
  let blockDom: string | null = null

  page.on('console', (msg) => {
    consoleLines.push(`[console:${msg.type()}] ${msg.text()}`)
  })
  page.on('pageerror', (err) => {
    consoleLines.push(`[pageerror] ${err.message}`)
  })
  page.on('websocket', (ws) => {
    // Only the control plane is text; the data plane is binary frames on the
    // same socket (AD-1), which arrive here as a Buffer and are skipped —
    // never redacted, never printed, because PTY bytes can carry exactly
    // what a person typed (AGENTS.md).
    ws.on('framesent', (f) => {
      if (typeof f.payload !== 'string') return
      const r = redactFrame('sent', f.payload)
      if (r) frames.push(r)
    })
    ws.on('framereceived', (f) => {
      if (typeof f.payload !== 'string') return
      const r = redactFrame('received', f.payload)
      if (r) frames.push(r)
    })
  })

  return {
    async captureSnapshot() {
      if (page.isClosed()) return
      try {
        snapshot = await page.locator('html').ariaSnapshot()
      } catch (err) {
        snapshot ??= `(snapshot unavailable: ${String(err)})`
      }
    },
    hasSnapshot() {
      return snapshot !== null
    },
    async captureBlockDom() {
      try {
        blockDom = await readBlockDomFrom(page)
      } catch (err) {
        // `readBlockDomFrom` already answers a refused read with a line of its
        // own, so this catches what is left of it. A diagnostic may never take
        // the report it is written into down with it (nocx-itmo2).
        blockDom = `(could not read the DOM: ${String(err)})`
      }
    },
    hasBlockDom() {
      return blockDom !== null
    },
    sections(label) {
      return [
        linesSection(
          `${label}: browser console & page errors`,
          consoleLines.items,
          consoleLines.dropped,
        ),
        linesSection(
          `${label}: control-plane frames (method, id, error only — never params, result or PTY bytes)`,
          frames.items.map(formatFrame),
          frames.dropped,
        ),
        `-- ${label}: accessibility snapshot --\n${snapshot ?? '(the page closed before a snapshot was taken)'}`,
        `-- ${label}: block DOM --\n${blockDom ?? '(the DOM was not read before the page went away)'}`,
      ]
    },
  }
}

/**
 * Put a page on the running test's failure report.
 *
 * The `page` fixture does this for the page it hands out. A spec that builds
 * its OWN client from `browser` calls it for each one (harness.ts's
 * `watchClient` does it with the running test's id).
 *
 * `closeAfterReport` hands the page's context to the harness: it is
 * snapshotted and closed at teardown, AFTER the report is written, so the
 * spec must not close it in its own `finally` — that runs first, and would
 * leave nothing to read. A spec that closes it ON PURPOSE mid-test still may;
 * the report then carries what the page said up to that moment.
 */
export function watchPageForTest(
  testId: string,
  page: Page,
  label: string,
  opts: { closeAfterReport: boolean; diagnostics?: PageDiagnostics },
): PageDiagnostics {
  const diagnostics = opts.diagnostics ?? attachFailureDiagnostics(page)
  const list = pagesByTestId.get(testId) ?? []
  list.push({ label, page, diagnostics, closeAfterReport: opts.closeAfterReport })
  pagesByTestId.set(testId, list)
  return diagnostics
}

/**
 * Snapshot every page this test put on the report, now.
 *
 * For a spec whose `finally` changes what its pages show before the report
 * is written — stopping the backend it brought paints the reconnect overlay
 * over every client, and a snapshot of THAT describes the teardown rather
 * than the failure. Such a spec calls this first thing in its `finally`.
 */
export async function captureSnapshotsForTest(testId: string): Promise<void> {
  for (const watched of pagesByTestId.get(testId) ?? []) {
    await watched.diagnostics.captureSnapshot()
  }
}

/**
 * Print the block if, and only if, `info` describes a test that did not end
 * the way it was expected to — then release everything this test put on the
 * report, closing the contexts the harness was handed. Called exactly once
 * per test, from the harness's auto fixture.
 *
 * `expectedStatus`, not `'passed'`: a skipped test was expected to skip, and
 * printing a FAILURE CONTEXT for it was noise in every CI log.
 */
export async function reportFailureContext(info: TestInfo, traceId: string): Promise<void> {
  const pages = pagesByTestId.get(info.testId) ?? []
  const backends = backendsByTestId.get(info.testId) ?? []
  const censuses = rowsCensusByTestId.get(info.testId) ?? []
  pagesByTestId.delete(info.testId)
  backendsByTestId.delete(info.testId)
  rowsCensusByTestId.delete(info.testId)
  try {
    if (info.status === info.expectedStatus) return

    const sections: string[] = [
      '',
      `FAILURE CONTEXT — ${info.titlePath.slice(1).join(' › ')} — ` +
        `${info.status} (trace_id=${traceId || '(none)'})`,
    ]

    const sharedLines = traceId ? linesForTrace(standBackendLogTail(), traceId) : []
    sections.push(linesSection('shared stand backend log, filtered by trace_id', sharedLines))

    for (const backend of backends) {
      const lines = backend.logTail(40_000).split('\n')
      sections.push(linesSection(`this test's own backend (${backend.logFile})`, lines))
      if (backend.goroutineDump) {
        sections.push(`-- backend goroutine dump (${backend.logFile}) --\n${backend.goroutineDump}`)
      }
    }

    for (const watched of pages) {
      // A page still open with no snapshot yet is read now; one already
      // snapshotted keeps it (captureSnapshotsForTest, the page fixture).
      if (!watched.diagnostics.hasSnapshot()) await watched.diagnostics.captureSnapshot()
      // The block DOM, on the same rule as the snapshot above: a page the
      // fixture owns was read on its way out, one a spec owns is usually still
      // open and is read here. Either way this runs for a test that did NOT
      // end as expected, and a passing test never reaches it (nocx-itmo2).
      if (!watched.diagnostics.hasBlockDom()) await watched.diagnostics.captureBlockDom()
      // Both are read BEFORE this page's sections are built, or a page read
      // here would print the "not read" line and then be read for nothing.
      sections.push(...watched.diagnostics.sections(watched.label))
    }
    if (pages.length === 0) {
      sections.push('-- no browser page was on this report --')
      // AND THE BLOCK DOM SAYS SO RATHER THAN GOING MISSING. The section is
      // not conditional on there being a page to read: a failed test whose
      // report omitted it reads as "the DOM was fine", which is the opposite
      // of what an absent read means — and the reader who concludes the DOM
      // was fine is the reader this whole section was written for. Reachable
      // whenever no page reached the report: a failure in beforeAll/afterAll,
      // a page fixture whose setup threw, or a spec that builds its own client
      // and never puts it on the report (nocx-itmo2).
      sections.push(
        '-- page: block DOM --\n(no browser page was on this report — the DOM was not read)',
      )
    }

    // The store's own answer about the rows the spec was comparing (nocx-rb4ca),
    // printed after the pages because it is read off the ledger rather than the
    // browser. A test that never compared stored rows records none, and says so
    // in one line instead of leaving the reader to wonder whether it was lost.
    if (censuses.length === 0) {
      sections.push(
        '-- ledger: stored rows of this test (nocx-rb4ca) --\n' +
          '(the spec recorded no census: it takes one only when its own comparison of stored rows fails)',
      )
    } else {
      for (const census of censuses) {
        sections.push(`-- ledger: stored rows at ${census.label} (nocx-rb4ca) --\n${census.text}`)
      }
    }

    process.stderr.write(sections.join('\n') + '\n')
  } finally {
    for (const watched of pages) {
      if (!watched.closeAfterReport) continue
      await watched.page
        .context()
        .close()
        .catch(() => undefined)
    }
  }
}

/** Render one source's lines, bounded to the last MAX_BACKEND_LINES with a
 *  note of how many earlier ones were not shown — on top of whatever the
 *  source itself already dropped (`extraDropped`, from a bounded() buffer
 *  collected live). */
function linesSection(label: string, lines: string[], extraDropped = 0): string {
  const shown = lines.length > MAX_BACKEND_LINES ? lines.slice(-MAX_BACKEND_LINES) : lines
  const truncated = lines.length - shown.length
  const totalDropped = truncated + extraDropped
  const header = `-- ${label} (${shown.length} shown${totalDropped > 0 ? `, ${totalDropped} dropped` : ''}) --`
  if (shown.length === 0) return `${header}\n(none)`
  return `${header}\n${shown.join('\n')}`
}

/** How long the block-DOM probe may take before the report moves on without
 *  it. A page whose main thread is wedged answers no `evaluate` at all, and a
 *  report that waits for one loses every section beside it. */
const BLOCK_DOM_DEADLINE_MS = 5_000

/**
 * Read the block DOM off `page` (nocx-itmo2), and never let it cost anything
 * but itself: a page that has gone away, a probe that rejects and a page that
 * simply stops answering are all answered with a line saying so. The report is
 * already describing one failure, and a diagnostic that is missing must not
 * replace it with a second one.
 */
async function readBlockDomFrom(page: Page): Promise<string> {
  if (page.isClosed()) return '(the page had closed before the DOM could be read)'
  try {
    const read = page.evaluate(readBlockDom)
    const timedOut = new Promise<string>((resolve) => {
      const timer = setTimeout(
        () => resolve('(the DOM read timed out — the page stopped answering)'),
        BLOCK_DOM_DEADLINE_MS,
      )
      timer.unref()
    })
    return await Promise.race([read, timedOut])
  } catch (err) {
    return `(could not read the DOM: ${String(err)})`
  }
}

/**
 * The block-DOM probe, run INSIDE the failing page.
 *
 * Why it exists, in one sentence: a CI run failed on
 * `.pane.active .scrollback-inner > .cmd-block:not(.cmd-block-running)` six
 * times over thirty seconds — zero elements every poll — while the
 * accessibility snapshot taken at the same moment showed the block settled on
 * screen. A snapshot flattens the DOM, so it cannot say whether
 * `.scrollback-inner` exists at all, whether the block hangs from it, or from
 * what else it hangs instead. This says exactly that.
 *
 * It runs in the browser, so it may not close over anything from this module:
 * Playwright serializes the function and nothing else travels with it.
 */
function readBlockDom(): string {
  /** A page can hold hundreds of restored blocks. Enough to see the shape,
   *  few enough to stay a short block in a CI log. */
  const MAX_BLOCKS = 20
  /** "up to four levels" of parent, the depth this diagnostic was asked for. */
  const CHAIN_LIMIT = 4

  const nodes = (selector: string): Element[] => Array.from(document.querySelectorAll(selector))
  const count = (selector: string): number => document.querySelectorAll(selector).length

  /** `tag.class1.class2`, the spelling a person reads in devtools. */
  const describe = (node: Element): string => {
    const classes = typeof node.className === 'string' ? node.className.trim().split(/\s+/) : []
    const tag = node.tagName.toLowerCase()
    return classes.length > 0 && classes[0] !== '' ? `${tag}.${classes.join('.')}` : tag
  }

  const parentChain = (node: Element): string => {
    const levels: string[] = []
    let parent = node.parentElement
    while (parent !== null && levels.length < CHAIN_LIMIT) {
      levels.push(describe(parent))
      parent = parent.parentElement
    }
    return levels.length > 0 ? levels.join(' < ') : '(no parent element)'
  }

  const blocks = nodes('.cmd-block')
  const lines: string[] = [`every .cmd-block on the page: ${blocks.length}`]
  for (const block of blocks.slice(0, MAX_BLOCKS)) {
    lines.push(
      `  ${describe(block)} entry-id=${JSON.stringify(block.getAttribute('data-entry-id'))}` +
        ` block-kind=${JSON.stringify(block.getAttribute('data-block-kind'))}` +
        ` running=${block.classList.contains('cmd-block-running')}`,
    )
    lines.push(`    parents (nearest first, at most ${CHAIN_LIMIT}): ${parentChain(block)}`)
  }
  if (blocks.length > MAX_BLOCKS) {
    lines.push(`  … ${blocks.length - MAX_BLOCKS} more .cmd-block not shown`)
  }

  // The selector that failed, taken apart step by step. A 0 on any line above
  // the last explains the failure on its own — the container is missing, or
  // the block is not its child. A 0 on the LAST line alone means the block is
  // a direct child of the container and still carries `cmd-block-running`.
  lines.push('selector counts:')
  for (const selector of [
    '.pane',
    '.pane.active',
    '.scrollback-area',
    '.scrollback-inner',
    '.pane.active .scrollback-inner',
    '.pane.active .scrollback-inner > .cmd-block',
    '.pane.active .scrollback-inner > .cmd-block:not(.cmd-block-running)',
  ]) {
    const matches = count(selector)
    lines.push(`  ${selector} = ${matches} ${matches > 0 ? '(matches)' : '(no match)'}`)
  }
  return lines.join('\n')
}
