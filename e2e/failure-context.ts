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
 * Four sources, each bounded so a chatty test cannot bury the one line that
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
}

const backendsByTestId = new Map<string, RegisterableBackend[]>()

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
 *  snapshot can still be taken. */
export interface PageDiagnostics {
  /** Read the page's accessibility tree NOW, while it is still open, and keep
   *  it for the report. A fixture page is closed by the time the auto fixture
   *  reports, so the `page` fixture calls this on its own way out; a closed
   *  page keeps whatever was taken last. */
  captureSnapshot(): Promise<void>
  /** Whether a snapshot has been taken already. */
  hasSnapshot(): boolean
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
  pagesByTestId.delete(info.testId)
  backendsByTestId.delete(info.testId)
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
    }

    for (const watched of pages) {
      // A page still open with no snapshot yet is read now; one already
      // snapshotted keeps it (captureSnapshotsForTest, the page fixture).
      if (!watched.diagnostics.hasSnapshot()) await watched.diagnostics.captureSnapshot()
      sections.push(...watched.diagnostics.sections(watched.label))
    }
    if (pages.length === 0) sections.push('-- no browser page was on this report --')

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
