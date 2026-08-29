// Resolution: a detected path plus the tab's origin → the absolute path the
// filesystem provider is asked for, or a named refusal. Every refusal is a
// case the UI has to SAY something about — a click that silently does
// nothing is the defect this whole feature exists to remove.
import { describe, expect, it } from 'vitest'
import { resolvePath } from './resolve'

const cwd = { cwd: '/Users/a/repo', cwdVerified: true }

describe('resolvePath', () => {
  it('passes an absolute path through', () => {
    expect(resolvePath({ kind: 'path', path: '/etc/hosts' }, cwd)).toEqual({
      ok: true,
      absolute: '/etc/hosts',
    })
  })

  it('joins a relative path onto the verified cwd', () => {
    expect(resolvePath({ kind: 'path', path: 'docs/architecture.md' }, cwd)).toEqual({
      ok: true,
      absolute: '/Users/a/repo/docs/architecture.md',
    })
  })

  it('resolves ./ and ../ against the cwd', () => {
    expect(resolvePath({ kind: 'path', path: './Makefile' }, cwd)).toEqual({
      ok: true,
      absolute: '/Users/a/repo/Makefile',
    })
    expect(resolvePath({ kind: 'path', path: '../other/x.go' }, cwd)).toEqual({
      ok: true,
      absolute: '/Users/a/other/x.go',
    })
  })

  it('normalises redundant separators and dot segments', () => {
    expect(resolvePath({ kind: 'path', path: 'a//b/./c/../d' }, cwd)).toEqual({
      ok: true,
      absolute: '/Users/a/repo/a/b/d',
    })
  })

  it('does not climb above the root', () => {
    expect(resolvePath({ kind: 'path', path: '../../../../../../etc' }, cwd)).toEqual({
      ok: true,
      absolute: '/etc',
    })
  })

  it('refuses a relative path when the cwd was never verified', () => {
    // An OSC 7 the shell never sent is not a cwd — joining onto a guess
    // opens the wrong file, which is worse than saying so.
    expect(
      resolvePath({ kind: 'path', path: 'docs/x.md' }, { cwd: '/Users/a', cwdVerified: false }),
    ).toEqual({ ok: false, reason: 'no-cwd' })
  })

  it('still resolves an absolute path with no verified cwd', () => {
    expect(
      resolvePath({ kind: 'path', path: '/etc/hosts' }, { cwd: '', cwdVerified: false }),
    ).toEqual({ ok: true, absolute: '/etc/hosts' })
  })

  it('expands ~ when the home directory is known', () => {
    expect(resolvePath({ kind: 'path', path: '~/.zshrc' }, { ...cwd, home: '/Users/a' })).toEqual({
      ok: true,
      absolute: '/Users/a/.zshrc',
    })
  })

  it('refuses ~ when the home directory is not known', () => {
    expect(resolvePath({ kind: 'path', path: '~/.zshrc' }, cwd)).toEqual({
      ok: false,
      reason: 'no-home',
    })
  })
})

describe('resolvePath — home derivation is not its job', () => {
  it('treats a bare ~ prefix without a slash as a normal relative path', () => {
    // The grammar only ever emits `~/`-prefixed paths, so this is defensive:
    // whatever else arrives is joined, never guessed at.
    expect(resolvePath({ kind: 'path', path: '~weird/x' }, cwd)).toEqual({
      ok: true,
      absolute: '/Users/a/repo/~weird/x',
    })
  })
})
