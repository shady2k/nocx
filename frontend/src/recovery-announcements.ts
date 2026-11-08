/**
 * What this window has already told a person is missing from one session
 * (nocx-ipuy3).
 *
 * A reclaim reports the same hole every time the window comes back: the
 * session's own retention bound dropped those bytes once, and nothing about
 * the loss changes when the pane is restored again. Saying it again reads as
 * the close losing output, so the byte ranges already named are remembered
 * per session and subtracted from the next announcement — only a hole that
 * grew is news, and then only by its growth.
 *
 * WHY localStorage, and why here rather than in the backend: this is renderer
 * presentation state — "has this person been told about these bytes" — and it
 * is keyed by the session it is about, so two windows cannot silence each
 * other's sessions. It must survive a restart, which is exactly the state the
 * defect is about: a fresh renderer meets the same session again. It is per
 * machine for the same reason IntegrationSilenceStore is (integration/
 * status.ts): a synced setting would carry one machine's reading to another.
 *
 * NOTHING HERE DECIDES WHAT IS MISSING. The ranges are the ones the card
 * stated, derived by recovery-account.ts, which is also what the e2e suite
 * compares against the ledger. This module only remembers them.
 */

/** The part of Storage this needs, so a test can hand it a Map-backed one. */
export interface RecoveryAnnouncementStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
}

/** One stretch of the session's byte stream a person has been told about. */
export interface AnnouncedRange {
  start: number
  end: number
}

/** Versioned, like every other localStorage document here: a different shape
 *  is a different key, never a migration of somebody's stored bytes. */
const KEY = 'nocx.recovery.announced.v1'

/** How many sessions' worth of announcements are kept. A session id is never
 *  reused, so an old entry is dead weight; this bounds the dead weight without
 *  ever forgetting a session the window can still show. */
const MAX_SESSIONS = 64

/** One session's announced ranges, oldest session first. The array order is
 *  the recency order the cap drops by, so it doubles as the eviction queue. */
type SessionEntry = { session: string; ranges: AnnouncedRange[] }

export class RecoveryAnnouncementStore {
  constructor(private readonly storage: RecoveryAnnouncementStorage | null) {}

  /** The ranges already announced for this session, or none. */
  announced(sessionId: string): AnnouncedRange[] {
    return this.read().find((entry) => entry.session === sessionId)?.ranges ?? []
  }

  /** Add the ranges this window has just named. Union, never replace: the
   *  hole only ever gains bytes, and a later announcement must not un-say an
   *  earlier one. */
  record(sessionId: string, ranges: readonly AnnouncedRange[]): void {
    if (this.storage === null || sessionId === '') return
    const entries = this.read().filter((entry) => entry.session !== sessionId)
    const previous = this.read().find((entry) => entry.session === sessionId)?.ranges ?? []
    const merged = mergeRanges([...previous, ...ranges])
    if (merged.length === 0) return
    entries.push({ session: sessionId, ranges: merged })
    this.write(entries.slice(-MAX_SESSIONS))
  }

  private read(): SessionEntry[] {
    if (this.storage === null) return []
    let raw: string | null
    try {
      raw = this.storage.getItem(KEY)
    } catch {
      return []
    }
    if (!raw) return []
    let parsed: unknown
    try {
      parsed = JSON.parse(raw)
    } catch {
      return []
    }
    if (!Array.isArray(parsed)) return []
    // A line that does not read as an entry is dropped rather than repaired:
    // a corrupt record means "nothing was announced", which offers the card
    // again — the safe direction.
    const entries: SessionEntry[] = []
    for (const item of parsed) {
      if (!Array.isArray(item) || item.length !== 2) continue
      const [session, ranges] = item as [unknown, unknown]
      if (typeof session !== 'string' || !Array.isArray(ranges)) continue
      const valid: AnnouncedRange[] = []
      for (const range of ranges) {
        if (!Array.isArray(range) || range.length !== 2) continue
        const [start, end] = range as [unknown, unknown]
        if (typeof start !== 'number' || typeof end !== 'number') continue
        valid.push({ start, end })
      }
      entries.push({ session, ranges: mergeRanges(valid) })
    }
    return entries
  }

  private write(entries: readonly SessionEntry[]): void {
    if (this.storage === null) return
    try {
      this.storage.setItem(
        KEY,
        JSON.stringify(entries.map((entry) => [entry.session, rangesAsPairs(entry.ranges)])),
      )
    } catch {
      // Storage full or denied. The hole is offered again next time, which is
      // the safe direction and exactly what happened before this existed.
    }
  }
}

/** Sorted, non-overlapping ranges. A range that runs backwards or is not a
 *  finite number is dropped, not repaired: it is a claim of unknown size. */
function mergeRanges(ranges: readonly AnnouncedRange[]): AnnouncedRange[] {
  const valid = ranges
    .filter(
      (range) =>
        Number.isFinite(range.start) && Number.isFinite(range.end) && range.end > range.start,
    )
    .map((range) => ({ start: range.start, end: range.end }))
    .sort((a, b) => a.start - b.start)
  const merged: AnnouncedRange[] = []
  for (const range of valid) {
    const last = merged[merged.length - 1]
    if (last !== undefined && range.start <= last.end) {
      last.end = Math.max(last.end, range.end)
    } else {
      merged.push(range)
    }
  }
  return merged
}

function rangesAsPairs(ranges: readonly AnnouncedRange[]): Array<[number, number]> {
  return ranges.map((range) => [range.start, range.end])
}

/** localStorage when the webview allows it, null when it does not. A denied
 *  storage must not take the pane down on construction. */
export function safeRecoveryStorage(): RecoveryAnnouncementStorage | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}
