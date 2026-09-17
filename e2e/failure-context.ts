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
 *   - The browser's console messages and uncaught page errors.
 *   - The control-plane's own JSON-RPC frames — method, id and error ONLY.
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

export interface FailureDiagnostics {
  /** Print the block if, and only if, info describes a test that just
   *  failed. Safe to call unconditionally at teardown. */
  report(info: TestInfo, traceId: string): Promise<void>
}

/**
 * Start listening on `page` for everything this module can print, and
 * return the reporter to call at teardown.
 *
 * Listeners are attached immediately — before navigation — so nothing the
 * page does before its first assertion is missed. Collecting is unconditional
 * (a passing test costs a few event-listener calls); only `report` checks
 * whether anything should be PRINTED.
 */
export function attachFailureDiagnostics(page: Page): FailureDiagnostics {
  const consoleLines = bounded<string>(MAX_CONSOLE_LINES)
  const frames = bounded<RedactedFrame>(MAX_FRAMES)

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
    async report(info, traceId) {
      if (info.status === 'passed') return

      const sections: string[] = [
        '',
        `FAILURE CONTEXT — ${info.titlePath.slice(1).join(' › ')} — ` +
          `${info.status} (trace_id=${traceId || '(none)'})`,
      ]

      const sharedLines = traceId ? linesForTrace(standBackendLogTail(), traceId) : []
      sections.push(linesSection('shared stand backend log, filtered by trace_id', sharedLines))

      for (const backend of backendsByTestId.get(info.testId) ?? []) {
        const lines = backend.logTail(40_000).split('\n')
        sections.push(linesSection(`this test's own backend (${backend.logFile})`, lines))
      }

      sections.push(
        linesSection('browser console & page errors', consoleLines.items, consoleLines.dropped),
      )
      sections.push(
        linesSection(
          'control-plane frames (method, id, error only — never params, result or PTY bytes)',
          frames.items.map(formatFrame),
          frames.dropped,
        ),
      )

      let snapshot: string
      try {
        snapshot = page.isClosed()
          ? '(page already closed; no snapshot)'
          : await page.locator('html').ariaSnapshot()
      } catch (err) {
        snapshot = `(snapshot unavailable: ${String(err)})`
      }
      sections.push(`-- accessibility snapshot --\n${snapshot}`)

      process.stderr.write(sections.join('\n') + '\n')
    },
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
