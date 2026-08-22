// The clock half of a transfer's wording. One owner, so "14 s" and
// "2 min ago" mean the same thing in every surface that prints them.
import { describe, expect, it } from 'vitest'
import { formatDuration, formatRelativeTime, formatTimestamp } from './format-time'

describe('how long it took', () => {
  it('counts milliseconds under a second, because the question is whether it was instant', () => {
    expect(formatDuration(0)).toBe('0 ms')
    expect(formatDuration(120)).toBe('120 ms')
    expect(formatDuration(999)).toBe('999 ms')
  })

  it('keeps one decimal under ten seconds, where it still distinguishes two transfers', () => {
    expect(formatDuration(1000)).toBe('1.0 s')
    expect(formatDuration(3450)).toBe('3.5 s')
  })

  it('drops the decimal above ten seconds, where nobody is reading it', () => {
    expect(formatDuration(14_000)).toBe('14 s')
    expect(formatDuration(42_400)).toBe('42 s')
  })

  it('never prints a value its own unit cannot hold', () => {
    // 59.6 s rounds to 60 seconds, and "60 s" is a minute wearing the wrong
    // label. The unit is decided on the ROUNDED value for exactly this.
    expect(formatDuration(59_600)).toBe('1 min')
  })

  it('splits minutes and hours, and drops an empty remainder', () => {
    expect(formatDuration(90_000)).toBe('1 min 30 s')
    expect(formatDuration(120_000)).toBe('2 min')
    expect(formatDuration(3_600_000)).toBe('1 h')
    expect(formatDuration(3_780_000)).toBe('1 h 3 min')
  })

  it('says nothing rather than "NaN s" for a duration that is not one', () => {
    expect(formatDuration(-1)).toBe('')
    expect(formatDuration(Number.NaN)).toBe('')
    expect(formatDuration(Number.POSITIVE_INFINITY)).toBe('')
  })
})

describe('when it happened', () => {
  const NOW = 1_700_000_000_000

  it('reads "just now" for the first three quarters of a minute', () => {
    expect(formatRelativeTime(NOW, NOW)).toBe('just now')
    expect(formatRelativeTime(NOW - 44_000, NOW)).toBe('just now')
  })

  it('steps to minutes, then hours, then days', () => {
    expect(formatRelativeTime(NOW - 60_000, NOW)).toBe('1 min ago')
    expect(formatRelativeTime(NOW - 5 * 60_000, NOW)).toBe('5 min ago')
    expect(formatRelativeTime(NOW - 2 * 3_600_000, NOW)).toBe('2 h ago')
    expect(formatRelativeTime(NOW - 3 * 86_400_000, NOW)).toBe('3 d ago')
  })

  it('reads a moment in the future as "just now", not as a negative age', () => {
    // Two clocks with nothing synchronising them: the store's, which stamps
    // the record, and the surface's, which ticks. A few milliseconds of
    // skew is ordinary, and "in -1 min" would be a fault report about
    // arithmetic rather than an answer to the question asked.
    expect(formatRelativeTime(NOW + 5_000, NOW)).toBe('just now')
  })

  it('says nothing when either end of the comparison is not a time', () => {
    expect(formatRelativeTime(Number.NaN, NOW)).toBe('')
    expect(formatRelativeTime(NOW, Number.NaN)).toBe('')
  })
})

describe('the exact moment, for the hover behind the label', () => {
  it('is the reader’s own locale rendering of that instant', () => {
    const at = Date.UTC(2026, 7, 22, 12, 34, 56)
    expect(formatTimestamp(at)).toBe(new Date(at).toLocaleString())
  })

  it('says nothing for a non-time', () => {
    expect(formatTimestamp(Number.NaN)).toBe('')
  })
})
