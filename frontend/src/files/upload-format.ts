// How a transfer's numbers are worded. One owner, because "1.2 MB" is the
// kind of derivation that gets rewritten slightly differently in every
// surface that needs it, and then two places disagree about what a
// megabyte is.
//
// Decimal units, not binary: the wire counts bytes and a person comparing
// nocx's number against the file's size in Finder or `ls -lh --si` should
// see the same one. The binary form is right for memory and wrong for a
// file on a disk.

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

/** One transfer's progress line: what has arrived, out of what, and how
 *  fast — each part present only when it is known. An unobserved byte
 *  count renders the size alone, because "0 B of 400 MB" is a claim that
 *  nothing has happened and the truth is that nothing has been SEEN. */
export function formatProgress(t: {
  bytes: number | null
  size: number
  speedBytesPerSecond: number | null
}): string {
  const total = formatBytes(t.size)
  const head = t.bytes === null ? total : `${formatBytes(t.bytes)} of ${total}`
  const speed = formatSpeed(t.speedBytesPerSecond)
  return speed === '' ? head : `${head} · ${speed}`
}
