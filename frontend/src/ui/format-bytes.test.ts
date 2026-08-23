import { describe, expect, it } from 'vitest'
import { formatBytes, formatFinished, formatProgress, formatSpeed } from './format-bytes'

describe('byte counts as a person reads them', () => {
  it('is exact below a kilobyte', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(999)).toBe('999 B')
  })

  it('steps up in decimal units, matching what a file manager shows', () => {
    expect(formatBytes(1_000)).toBe('1.0 kB')
    expect(formatBytes(1_500_000)).toBe('1.5 MB')
    expect(formatBytes(2_400_000_000)).toBe('2.4 GB')
  })

  it('clamps at the largest unit it names rather than inventing one', () => {
    expect(formatBytes(5_000_000_000_000_000)).toBe('5000.0 TB')
  })
})

describe('the rate', () => {
  it('is nothing at all when there is no speed', () => {
    // Not "0 B/s", which is a claim that the transfer has stalled.
    expect(formatSpeed(null)).toBe('')
  })

  it('is the byte count per second otherwise', () => {
    expect(formatSpeed(1_200_000)).toBe('1.2 MB/s')
  })
})

describe('the progress line', () => {
  it('says the size alone while nothing has been observed', () => {
    expect(formatProgress({ done: null, total: 400_000_000, speedBytesPerSecond: null })).toBe(
      '400.0 MB',
    )
  })

  it('says what has arrived out of what, once something has', () => {
    expect(formatProgress({ done: 1_000_000, total: 4_000_000, speedBytesPerSecond: null })).toBe(
      '1.0 MB of 4.0 MB',
    )
  })

  it('adds the rate when there is one', () => {
    expect(
      formatProgress({ done: 1_000_000, total: 4_000_000, speedBytesPerSecond: 500_000 }),
    ).toBe('1.0 MB of 4.0 MB · 500.0 kB/s')
  })
})

describe('the progress line when the size was never declared', () => {
  it('says what has arrived, and never "0 B" for a size nobody stated', () => {
    // A transfer adopted from a retained outcome after a reload was never
    // told how big it is. Zero is a measurement; this is its absence.
    expect(formatProgress({ done: 4_000, total: null, speedBytesPerSecond: null })).toBe('4.0 kB')
  })

  it('falls back to the rate alone when nothing at all is known about the bytes', () => {
    expect(formatProgress({ done: null, total: null, speedBytesPerSecond: 500_000 })).toBe(
      '500.0 kB/s',
    )
  })

  it('says nothing when nothing is known', () => {
    expect(formatProgress({ done: null, total: null, speedBytesPerSecond: null })).toBe('')
  })
})

// A finished row used to read `appicon.png · Done · /home/dev` — a name, a
// word and a path. Somebody coming back to the list learnt nothing from it:
// not how big the thing was, not when it landed, not how long it took
// (owner, 2026-08-22).
describe('the finished line', () => {
  const ENDED = 1_700_000_000_000

  it('says how big it was, when it landed, and how long it took', () => {
    expect(
      formatFinished({
        total: 4_000_000,
        startedAt: ENDED - 14_000,
        endedAt: ENDED,
        now: ENDED + 5 * 60_000,
      }),
    ).toBe('4.0 MB · 5 min ago · took 14 s')
  })

  it('drops the duration when nobody saw it start', () => {
    // An adopted transfer has an end and no beginning, and a span needs
    // both ends: "took 0 ms" would answer a question nobody could answer.
    expect(formatFinished({ total: 4_000_000, startedAt: null, endedAt: ENDED, now: ENDED })).toBe(
      '4.0 MB · just now',
    )
  })

  it('drops the size when none was declared', () => {
    expect(
      formatFinished({ total: null, startedAt: ENDED - 1_000, endedAt: ENDED, now: ENDED }),
    ).toBe('just now · took 1.0 s')
  })

  it('says nothing at all when it knows nothing at all', () => {
    // And the row then draws no line, rather than an empty one.
    expect(formatFinished({ total: null, startedAt: null, endedAt: null, now: ENDED })).toBe('')
  })

  it('keeps an empty file’s size, because an empty file is a file', () => {
    expect(formatFinished({ total: 0, startedAt: null, endedAt: null, now: ENDED })).toBe('0 B')
  })
})
