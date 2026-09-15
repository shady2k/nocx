import { describe, expect, it } from 'vitest'
import { cwdLabel } from './cwd-label'

describe('cwdLabel — without a known home, the absolute path (spec 2026-09-15 §2)', () => {
  it.each([
    ['/home/dev/repos/nocx', '/home/dev/repos/nocx'],
    ['/home/dev/repos/nocx/', '/home/dev/repos/nocx'],
    ['/srv', '/srv'],
    ['/', '~'],
    ['~', '~'],
    ['', '~'],
    ['   ', '~'],
  ])('%j → %j', (cwd, label) => {
    expect(cwdLabel(cwd)).toBe(label)
  })

  it('never guesses a `~` for a path that merely looks like a home directory', () => {
    // No home was ever reported for this session — the absolute path is the
    // whole truth available, and shortening it would assert something nobody
    // verified.
    expect(cwdLabel('/home/dev')).toBe('/home/dev')
  })

  it('collapses more than four segments behind a `…/` keeping the last three', () => {
    expect(cwdLabel('/a/b/c/d/e/f')).toBe('/…/d/e/f')
  })

  it('keeps exactly four segments in full', () => {
    expect(cwdLabel('/a/b/c/d')).toBe('/a/b/c/d')
  })
})

describe('cwdLabel — with a known home', () => {
  it('the home itself is `~`', () => {
    expect(cwdLabel('/home/dutch', '/home/dutch')).toBe('~')
  })

  it('a trailing slash on either side changes nothing', () => {
    expect(cwdLabel('/home/dutch/', '/home/dutch')).toBe('~')
    expect(cwdLabel('/home/dutch/project', '/home/dutch/')).toBe('~/project')
  })

  it('a path under home is `~/rest`, in full up to four segments', () => {
    expect(cwdLabel('/home/dutch/project', '/home/dutch')).toBe('~/project')
    expect(cwdLabel('/home/dutch/a/b/c/d', '/home/dutch')).toBe('~/a/b/c/d')
  })

  it('more than four segments under home collapse behind `…/`, last three kept', () => {
    expect(cwdLabel('/home/dutch/a/b/c/d/e', '/home/dutch')).toBe('~/…/c/d/e')
  })

  it('a path outside home is the absolute path, not a shortened one', () => {
    expect(cwdLabel('/srv/other', '/home/dutch')).toBe('/srv/other')
  })

  it('a missing cwd is `~` regardless of a known home', () => {
    expect(cwdLabel('', '/home/dutch')).toBe('~')
    expect(cwdLabel('   ', '/home/dutch')).toBe('~')
  })
})
