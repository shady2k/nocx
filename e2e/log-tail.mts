/**
 * The tail of a log, and nothing else (nocx-atpw5.1.40).
 *
 * WHY. A dev build logs at debug from its first line (internal/log's
 * DefaultLevel), and the e2e job runs that build. Every log this suite keeps —
 * a backend a spec started for itself, the helper daemon serving its home, the
 * shared stand's backend — is therefore an unbounded debug log, and the copies
 * under test-results/ were kept whole. Measured on this machine, one passing
 * three-case spec left 17 MB of them (9.2 MB backend + 7.1 MB helper for its
 * longest case), and the stand's own log grows about 0.6 MB per minute of run.
 *
 * WHAT A READER NEEDS IS THE END. Both consumers of these files read a tail:
 * e2e/failure-context.ts filters the stand log to the failing test's trace id
 * and prints at most 20 000 characters of it, and `VaultBackend.logTail` prints
 * the last few thousand bytes of the backend's. The preserved copy is the same
 * file for a person to read after the run, and the interesting lines are the
 * ones nearest the failure. So the bound keeps the end and says how much it
 * dropped, rather than keeping the head and losing the answer.
 *
 * No Playwright import, and no dependency beyond node:fs — the same rule
 * e2e/trace-context.mts states, so `node --test e2e/log-tail.unit.mts` is the
 * whole test story for it.
 */
import { appendFileSync, readFileSync, statSync, writeFileSync } from 'node:fs'

/**
 * How much of one per-test log is kept: 8 MiB.
 *
 * Above every measured case with room to spare — the longest case of the
 * heaviest spec in the suite (transcript-scroll-budget's 500 blocks, ~4
 * minutes locally) wrote 9.2 MB of backend log — so an ordinary failure keeps
 * its whole account and only a pathologically chatty one is cut.
 */
export const PER_TEST_LOG_BYTES = 8 * 1024 * 1024

/**
 * How much of the shared stand's backend log is kept: 16 MiB, about half an
 * hour of run at the measured rate. The stand outlives every test, so this is
 * the one log a whole shard writes; the failure report reads the last 20 000
 * characters of it, which a file this size always holds.
 */
export const STAND_LOG_BYTES = 16 * 1024 * 1024

/** A marker that says the file was cut, in the file itself, so a reader is
 *  never surprised by a log that starts mid-line. */
function droppedMarker(dropped: number): string {
  return `…[${dropped} bytes of earlier log dropped to bound this file]\n`
}

/**
 * The last `maxBytes` bytes of `text`, starting on a line boundary, prefixed
 * with a line that says how much was dropped.
 *
 * Line boundary rather than a byte cut: a UTF-8 rune or half a log line at the
 * top of a file reads as corruption, and one discarded partial line costs
 * nothing a reader wanted.
 */
export function tailOf(text: string, maxBytes: number): string {
  const buf = Buffer.from(text, 'utf8')
  if (buf.length <= maxBytes) return text
  const cut = buf.subarray(buf.length - maxBytes)
  const newline = cut.indexOf(0x0a)
  const kept = newline >= 0 && newline + 1 < cut.length ? cut.subarray(newline + 1) : cut
  return droppedMarker(buf.length - kept.length) + kept.toString('utf8')
}

/**
 * Append `chunk` to the file at `path`, keeping the file's own tail within
 * `maxBytes`.
 *
 * Rewriting a file this size on every chunk would make the stand's log cost
 * quadratic in a run's output, so the truncation is amortised: the file is
 * allowed to grow a quarter past the bound before it is rewritten down to it.
 * The peak a running shard can hold is therefore 1.25x the bound, and the file
 * a reader opens is never larger than that either.
 */
export function appendCapped(path: string, chunk: string | Buffer, maxBytes: number): void {
  appendFileSync(path, chunk)
  let size: number
  try {
    size = statSync(path).size
  } catch {
    return
  }
  if (size <= maxBytes + maxBytes / 4) return
  writeFileSync(path, tailOf(readFileSync(path, 'utf8'), maxBytes))
}
