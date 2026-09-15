// The session-home source: one files.open binding per session, shared by
// every consumer that needs the session's home directory (the link opener
// today, the prompt line next). Moved here from
// terminal-links/open.test.ts (nocx-9bpeq.13) along with the module itself.
import { describe, expect, it, vi } from 'vitest'
import { createSessionHomeSource, homeFromRoot, type SessionHomeDeps } from './session-home'
import type { FilesOpenResult } from '../generated/files.open'

function root(path: string, display: string): FilesOpenResult['root'] {
  return { path, display, inferred: false, inferredReason: '' }
}

function openResult(over: Partial<FilesOpenResult> = {}): FilesOpenResult {
  return {
    bindingId: 'b1',
    endpointId: null,
    root: root('/Users/a/repo', '~/repo'),
    revealAvailable: true,
    ...over,
  }
}

function deps(over: Partial<SessionHomeDeps> = {}): SessionHomeDeps & {
  liveness: Map<string, (live: boolean) => void>
} {
  const liveness = new Map<string, (live: boolean) => void>()
  return {
    liveness,
    openBinding: vi.fn(() => Promise.resolve(openResult())),
    onBindingLiveness: (bindingId, cb) => {
      liveness.set(bindingId, cb)
      return () => liveness.delete(bindingId)
    },
    ...over,
  }
}

describe('homeFromRoot', () => {
  it('reads home off the provider’s own tilde abbreviation', () => {
    expect(homeFromRoot(root('/Users/a/repo', '~/repo'))).toBe('/Users/a')
    expect(homeFromRoot(root('/Users/a', '~'))).toBe('/Users/a')
  })

  it('answers nothing for a root outside home', () => {
    expect(homeFromRoot(root('/etc', '/etc'))).toBeUndefined()
  })

  it('answers nothing when the abbreviation does not fit the path', () => {
    expect(homeFromRoot(root('/x', '~/a/very/long/thing'))).toBeUndefined()
  })
})

describe('createSessionHomeSource — ensure', () => {
  it('answers no home before any binding resolves', () => {
    const source = createSessionHomeSource(deps())
    expect(source.home('sess-1')).toBeUndefined()
  })

  it('derives the home once the binding resolves', async () => {
    const source = createSessionHomeSource(deps())
    await source.ensure('sess-1', '/Users/a/repo', true)
    expect(source.home('sess-1')).toBe('/Users/a')
  })

  it('opens one binding per session no matter how many callers ask', async () => {
    const d = deps()
    const source = createSessionHomeSource(d)
    await Promise.all([
      source.ensure('sess-1', '/Users/a/repo', true),
      source.ensure('sess-1', '/Users/a/repo', true),
    ])
    await source.ensure('sess-1', '/Users/a/repo', true)
    expect(d.openBinding).toHaveBeenCalledTimes(1)
  })

  it('passes the verified cwd as the rootPath and omits it when unverified', async () => {
    const d = deps()
    const source = createSessionHomeSource(d)
    await source.ensure('sess-1', '/Users/a/repo', true)
    expect(d.openBinding).toHaveBeenCalledWith('sess-1', '/Users/a/repo')
    await source.ensure('sess-2', '/Users/a/repo', false)
    expect(d.openBinding).toHaveBeenCalledWith('sess-2', undefined)
  })

  it('keeps sessions independent: two sessions get two bindings and two homes', async () => {
    const d = deps({
      openBinding: vi.fn((sessionId: string) =>
        Promise.resolve(
          sessionId === 'sess-1'
            ? openResult()
            : openResult({ bindingId: 'b2', root: root('/home/b/proj', '~/proj') }),
        ),
      ),
    })
    const source = createSessionHomeSource(d)
    await source.ensure('sess-1', '/Users/a/repo', true)
    await source.ensure('sess-2', '/home/b/proj', true)
    expect(source.home('sess-1')).toBe('/Users/a')
    expect(source.home('sess-2')).toBe('/home/b')
    expect(d.openBinding).toHaveBeenCalledTimes(2)
  })

  it('opens a fresh binding after the old one dies', async () => {
    const d = deps()
    const source = createSessionHomeSource(d)
    await source.ensure('sess-1', '/Users/a/repo', true)
    const kill = d.liveness.get('b1')
    kill?.(false)
    await source.ensure('sess-1', '/Users/a/repo', true)
    expect(d.openBinding).toHaveBeenCalledTimes(2)
  })

  it('does not cache a rejected open: the next call tries again', async () => {
    let calls = 0
    const d = deps({
      openBinding: () => {
        calls++
        return calls === 1 ? Promise.reject(new Error('nope')) : Promise.resolve(openResult())
      },
    })
    const source = createSessionHomeSource(d)
    await expect(source.ensure('sess-1', '/Users/a/repo', true)).rejects.toThrow('nope')
    await source.ensure('sess-1', '/Users/a/repo', true)
    expect(calls).toBe(2)
    expect(source.home('sess-1')).toBe('/Users/a')
  })

  it('never un-knows a home a later read could not derive', async () => {
    const d = deps({
      openBinding: vi
        .fn()
        .mockResolvedValueOnce(openResult())
        .mockResolvedValueOnce(openResult({ bindingId: 'b2', root: root('/etc', '/etc') })),
    })
    const source = createSessionHomeSource(d)
    await source.ensure('sess-1', '/Users/a/repo', true)
    expect(source.home('sess-1')).toBe('/Users/a')
    const kill = d.liveness.get('b1')
    kill?.(false)
    await source.ensure('sess-1', '/Users/a/repo', true)
    expect(source.home('sess-1')).toBe('/Users/a')
  })
})

describe('createSessionHomeSource — subscribe', () => {
  it('fires when a session’s home becomes known', async () => {
    const source = createSessionHomeSource(deps())
    const seen: (string | undefined)[] = []
    source.subscribe('sess-1', (home) => seen.push(home))
    await source.ensure('sess-1', '/Users/a/repo', true)
    expect(seen).toEqual(['/Users/a'])
  })

  it('does not replay a value already known before subscribing', async () => {
    const source = createSessionHomeSource(deps())
    await source.ensure('sess-1', '/Users/a/repo', true)
    const seen: (string | undefined)[] = []
    source.subscribe('sess-1', (home) => seen.push(home))
    expect(seen).toEqual([])
  })

  it('stops notifying after unsubscribe', async () => {
    const d = deps()
    const source = createSessionHomeSource(d)
    const seen: (string | undefined)[] = []
    const unsubscribe = source.subscribe('sess-1', (home) => seen.push(home))
    unsubscribe()
    d.liveness.get('b1')?.(false)
    await source.ensure('sess-1', '/Users/a/repo', true)
    expect(seen).toEqual([])
  })

  it('never fires for another session', async () => {
    const d = deps({
      openBinding: vi.fn((sessionId: string) =>
        Promise.resolve(
          sessionId === 'sess-2'
            ? openResult({ bindingId: 'b2', root: root('/home/b/proj', '~/proj') })
            : openResult(),
        ),
      ),
    })
    const source = createSessionHomeSource(d)
    const seen: (string | undefined)[] = []
    source.subscribe('sess-1', (home) => seen.push(home))
    await source.ensure('sess-2', '/home/b/proj', true)
    expect(seen).toEqual([])
  })
})
