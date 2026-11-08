// A block header's elapsed time, in one wording for a running and a finished
// block (nocx-ouru5.1.7). The owner saw "167m 36s" on a finished block: minutes
// were the largest unit the header had, so a long run read as arithmetic.
//
// The UNIT WORDS ARE THE CATALOGUE'S, so the same figure reads in Russian.
import { afterEach, describe, expect, it } from 'vitest'
import { setActiveLocale } from '../i18n/locale-model'
import { formatElapsed } from './elapsed'

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

describe('a block’s elapsed time', () => {
  afterEach(() => setActiveLocale('en'))

  it('keeps tenths of a second under a minute, as it always has', () => {
    expect(formatElapsed(12_800)).toBe('12.8s')
    // Below a second the figure is the milliseconds that were measured —
    // "0.0s" would read as no time at all (spec 2026-09-15 §1.7 round 3).
    expect(formatElapsed(27)).toBe('27ms')
  })

  it('counts minutes and seconds under an hour', () => {
    expect(formatElapsed(43 * MINUTE + 8_000)).toBe('43m 8s')
  })

  it('counts hours and minutes under a day', () => {
    expect(formatElapsed(2 * HOUR + 47 * MINUTE)).toBe('2h 47m')
  })

  it('counts days and hours from a day on', () => {
    expect(formatElapsed(DAY + 3 * HOUR)).toBe('1d 3h')
    expect(formatElapsed(7 * DAY)).toBe('7d 0h')
  })

  it('decides the unit on the figure it prints, so no unit holds a value it cannot', () => {
    expect(formatElapsed(MINUTE)).toBe('1m 0s')
    expect(formatElapsed(HOUR)).toBe('1h 0m')
    expect(formatElapsed(DAY)).toBe('1d 0h')
  })

  it('says the same time in Russian, with the units from the catalogue', () => {
    setActiveLocale('ru')
    expect(formatElapsed(12_800)).toBe('12.8 с')
    expect(formatElapsed(43 * MINUTE + 8_000)).toBe('43 мин 8 с')
    expect(formatElapsed(2 * HOUR + 47 * MINUTE)).toBe('2 ч 47 мин')
    expect(formatElapsed(DAY + 3 * HOUR)).toBe('1 д 3 ч')
    expect(formatElapsed(27)).toBe('27 мс')
  })

  it('says nothing rather than a figure for a duration that is not one', () => {
    expect(formatElapsed(-1)).toBe('')
    expect(formatElapsed(Number.NaN)).toBe('')
    expect(formatElapsed(Number.POSITIVE_INFINITY)).toBe('')
  })
})
