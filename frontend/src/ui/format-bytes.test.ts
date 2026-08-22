import { describe, expect, it } from 'vitest'
import { formatBytes, formatProgress, formatSpeed } from './format-bytes'

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
