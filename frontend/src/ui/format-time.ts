// How a transfer's CLOCK is worded — when something happened and how long
// it took. The other half of `format-bytes.ts`, and here for the same
// reason: "14 s" and "2 min ago" are exactly the derivations that get
// rewritten slightly differently in every surface that needs them, and then
// two places disagree about when an hour starts.
//
// A finished operation row wants three facts and this module owns two of
// them; `format-bytes.ts` owns the third and composes the line, so a caller
// asks one function and never assembles the sentence itself.
//
// ## Relative, not absolute, and why
//
// The operations list is read MINUTES after the work finished far more
// often than days after: somebody starts a 400 MB upload, does something
// else, and comes back to see whether it landed. "2 min ago" answers that
// without knowing what time it is now; "14:32" makes the reader do the
// subtraction, and does it worst in the case the list is actually for.
//
// The cost is that a relative label goes stale where an absolute one cannot
// — "just now" is a lie ninety seconds later if nothing repaints — so the
// clock is a PARAMETER here and the surface owns a ticking one
// (operations-view.tsx). A relative label with no clock behind it is the
// soft degrade AGENTS.md forbids, in miniature.
//
// The absolute form is not dropped; it rides the row's `title`, which is
// where a reader who wants the exact moment looks.

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/**
 * How long something took.
 *
 * The precision follows the magnitude, because the question changes with
 * it: under a second a person is asking whether it was instant, so
 * milliseconds; under ten seconds one decimal still distinguishes two
 * transfers; above that nobody cares about the fraction. Empty for a
 * duration that is not a duration — negative, or not a number — rather than
 * printing "NaN s".
 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return ''
  if (ms < SECOND) return `${Math.round(ms)} ms`
  if (ms < 10 * SECOND) return `${(ms / SECOND).toFixed(1)} s`
  // Rounded to seconds FIRST and branched on the result, so 59.6 s reads
  // "1 min" rather than "60 s" — the boundary is where a unit label is
  // decided, and deciding it on the unrounded value prints a value the
  // unit cannot hold.
  const seconds = Math.round(ms / SECOND)
  if (seconds < 60) return `${seconds} s`
  const minutes = Math.floor(seconds / 60)
  const restSeconds = seconds % 60
  if (minutes < 60) return restSeconds === 0 ? `${minutes} min` : `${minutes} min ${restSeconds} s`
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  return restMinutes === 0 ? `${hours} h` : `${hours} h ${restMinutes} min`
}

/**
 * When something happened, against a clock the caller supplies.
 *
 * `now` is a parameter and never `Date.now()` read inside: a surface that
 * renders this has to repaint it as it ages, so it owns the clock, and a
 * test that could not move the clock could only assert "just now".
 *
 * A moment in the FUTURE reads as "just now" rather than as a negative age.
 * The two clocks here are the store's and the surface's and nothing
 * synchronises them, so a few milliseconds of skew is ordinary; "in -1 min"
 * would be a fault report about arithmetic, which is not what the reader
 * asked.
 */
export function formatRelativeTime(at: number, now: number): string {
  if (!Number.isFinite(at) || !Number.isFinite(now)) return ''
  const age = Math.max(0, now - at)
  if (age < 45 * SECOND) return 'just now'
  if (age < 90 * SECOND) return '1 min ago'
  if (age < HOUR) return `${Math.round(age / MINUTE)} min ago`
  if (age < DAY) return `${Math.round(age / HOUR)} h ago`
  return `${Math.round(age / DAY)} d ago`
}

/** The exact moment, in the reader's own locale — the hover detail behind a
 *  relative label, never the label itself. */
export function formatTimestamp(at: number): string {
  if (!Number.isFinite(at)) return ''
  return new Date(at).toLocaleString()
}
