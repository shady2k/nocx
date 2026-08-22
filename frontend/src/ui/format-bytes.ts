// How a transfer's numbers are worded. One owner, because "1.2 MB" is the
// kind of derivation that gets rewritten slightly differently in every
// surface that needs it, and then two places disagree about what a
// megabyte is.
//
// It lives in the kit rather than beside the upload store because that is
// where the surfaces that render it are: OperationRow draws the line, and a
// kit component cannot reach into a feature module for its own wording.
// Moved here whole rather than copied — the reason above is the reason a
// second copy would have been the defect.
//
// Decimal units, not binary: the wire counts bytes and a person comparing
// nocx's number against the file's size in Finder or `ls -lh --si` should
// see the same one. The binary form is right for memory and wrong for a
// file on a disk.

import { formatDuration, formatRelativeTime } from './format-time'

const UNITS = ['B', 'kB', 'MB', 'GB', 'TB'] as const

/** A byte count, as a person reads it. Exact below a kilobyte — "512 B"
 *  is more useful than "0.5 kB" — and one decimal above it. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return ''
  let value = bytes
  let unit = 0
  while (value >= 1000 && unit < UNITS.length - 1) {
    value /= 1000
    unit++
  }
  if (unit === 0) return `${Math.round(value)} ${UNITS[0]}`
  return `${value.toFixed(1)} ${UNITS[unit]}`
}

/** A rate. Null in, empty out — a transfer with no samples has no speed,
 *  and rendering "0 B/s" for it would say it had stalled. */
export function formatSpeed(bytesPerSecond: number | null): string {
  if (bytesPerSecond === null) return ''
  return `${formatBytes(bytesPerSecond)}/s`
}

/** One operation's progress line: what has arrived, out of what, and how
 *  fast — each part present only when it is known. An unobserved byte
 *  count renders the size alone, because "0 B of 400 MB" is a claim that
 *  nothing has happened and the truth is that nothing has been SEEN. A
 *  null TOTAL is the same absence one level up — a transfer adopted from a
 *  retained outcome was never told how big it is — and renders whatever is
 *  known instead of "0 B", which would be a measurement nobody took. */
export function formatProgress(t: {
  done: number | null
  total: number | null
  speedBytesPerSecond: number | null
}): string {
  const total = t.total === null ? '' : formatBytes(t.total)
  const head =
    t.done === null
      ? total
      : total === ''
        ? formatBytes(t.done)
        : `${formatBytes(t.done)} of ${total}`
  const speed = formatSpeed(t.speedBytesPerSecond)
  if (head === '') return speed
  return speed === '' ? head : `${head} · ${speed}`
}

/**
 * One FINISHED operation's line: how big it was, when it ended, and how
 * long it took — the three facts a person coming back to the list is
 * actually asking for, and the three the row carried none of.
 *
 * The composer lives beside `formatProgress` because they are siblings:
 * the running line and the finished line, one per side of the same split,
 * and a surface that assembled either from parts would be the second author
 * of the separator, the order and which parts may be missing.
 *
 * EVERY PART IS OPTIONAL AND ABSENCE IS SILENT. An adopted transfer knows
 * neither its size nor when it started; a row that printed "0 B" or
 * "took 0 ms" for it would answer a question nobody could have answered.
 * All three missing yields '', and the surface draws no line at all.
 */
export function formatFinished(t: {
  total: number | null
  startedAt: number | null
  endedAt: number | null
  /** The clock the relative age is read against — see format-time.ts for
   *  why it is a parameter and who ticks it. */
  now: number
}): string {
  const parts: string[] = []
  if (t.total !== null) parts.push(formatBytes(t.total))
  if (t.endedAt !== null) parts.push(formatRelativeTime(t.endedAt, t.now))
  // A duration needs BOTH ends, which is the interval AGENTS.md asks for
  // stated as code: without a start there is no span, only a moment.
  if (t.endedAt !== null && t.startedAt !== null && t.endedAt >= t.startedAt) {
    parts.push(`took ${formatDuration(t.endedAt - t.startedAt)}`)
  }
  return parts.filter((p) => p !== '').join(' · ')
}
