import { describe, expect, it } from 'vitest'
import { cwdLabel } from './cwd-label'

describe('cwdLabel — the short form a block and the composer both show', () => {
  it.each([
    ['/home/dev/repos/nocx', 'repos/nocx'],
    ['/home/dev/repos/nocx/', 'repos/nocx'],
    ['/srv', 'srv'],
    ['/', '~'],
    ['~', '~'],
    ['', '~'],
    ['   ', '~'],
  ])('%j → %j', (cwd, label) => {
    expect(cwdLabel(cwd)).toBe(label)
  })
})
