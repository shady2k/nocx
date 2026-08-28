// The opener: a target plus the tab it was printed in → something the user
// can see happen. Every branch here ends in either an opened surface or a
// message — the one outcome that is not allowed is silence, which is what
// clicking a path in nocx did before this existed.
import { describe, expect, it, vi } from 'vitest'
import { createLinkOpener, homeFromRoot, type LinkOpenDeps } from './open'
import type { FilesOpenResult } from '../generated/files.open'
import type { ActiveOrigin } from '../pane-content'

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

const origin: Omit<ActiveOrigin, 'paneId'> = {
  sessionId: 'sess-1',
  kind: 'local',
  cwd: '/Users/a/repo',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
  machine: 'This machine',
}

function deps(over: Partial<LinkOpenDeps> = {}): LinkOpenDeps & {
  viewed: Parameters<LinkOpenDeps['openViewer']>[0][]
  said: string[]
} {
  const viewed: Parameters<LinkOpenDeps['openViewer']>[0][] = []
  const said: string[] = []
  return {
    viewed,
    said,
    openUrl: vi.fn(() => Promise.resolve()),
    openBinding: vi.fn(() => Promise.resolve(openResult())),
    openViewer: (t) => viewed.push(t),
    notify: (m) => said.push(m),
    onBindingLiveness: () => () => {},
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

describe('createLinkOpener — urls', () => {
  it('hands an http url to the system browser', async () => {
    const d = deps()
    await createLinkOpener(d).open({ kind: 'url', url: 'https://example.com/x' }, origin)
    expect(d.openUrl).toHaveBeenCalledWith('https://example.com/x')
    expect(d.viewed).toEqual([])
  })

  it('says so when the browser could not be reached', async () => {
    const d = deps({ openUrl: () => Promise.reject(new Error('no runtime')) })
    await createLinkOpener(d).open({ kind: 'url', url: 'https://example.com/x' }, origin)
    expect(d.said).toHaveLength(1)
    expect(d.said[0]).toContain('https://example.com/x')
  })
})

describe('createLinkOpener — files', () => {
  it('opens a viewer on the resolved absolute path', async () => {
    const d = deps()
    await createLinkOpener(d).open(
      { kind: 'path', path: 'docs/architecture.md', line: 101 },
      origin,
    )
    expect(d.viewed).toHaveLength(1)
    expect(d.viewed[0]).toMatchObject({
      bindingId: 'b1',
      path: '/Users/a/repo/docs/architecture.md',
      canonical: '/Users/a/repo/docs/architecture.md',
      name: 'architecture.md',
      line: 101,
      displayHost: null,
    })
  })

  it('binds against the session the link was printed in', async () => {
    const d = deps()
    await createLinkOpener(d).open({ kind: 'path', path: 'a.ts' }, origin)
    expect(d.openBinding).toHaveBeenCalledWith('sess-1', '/Users/a/repo')
  })

  it('carries the host so a remote file is labelled as one', async () => {
    const d = deps({ openBinding: () => Promise.resolve(openResult({ endpointId: 'ep1' })) })
    await createLinkOpener(d).open(
      { kind: 'path', path: '/srv/x.conf' },
      { ...origin, kind: 'ssh', host: 'srv-01' },
    )
    expect(d.viewed[0]).toMatchObject({ endpointId: 'ep1', displayHost: 'srv-01' })
  })

  it('reuses one binding for repeated clicks in the same session', async () => {
    const d = deps()
    const opener = createLinkOpener(d)
    await opener.open({ kind: 'path', path: 'a.ts' }, origin)
    await opener.open({ kind: 'path', path: 'b.ts' }, origin)
    expect(d.openBinding).toHaveBeenCalledTimes(1)
    expect(d.viewed).toHaveLength(2)
  })

  it('opens a fresh binding after the old one dies', async () => {
    let kill: (() => void) | undefined
    const d = deps({
      onBindingLiveness: (_id, cb) => {
        kill = () => cb(false)
        cb(true)
        return () => {}
      },
    })
    const opener = createLinkOpener(d)
    await opener.open({ kind: 'path', path: 'a.ts' }, origin)
    kill?.()
    await opener.open({ kind: 'path', path: 'b.ts' }, origin)
    expect(d.openBinding).toHaveBeenCalledTimes(2)
  })

  it('expands ~ from the home the binding’s root reveals', async () => {
    const d = deps()
    const opener = createLinkOpener(d)
    await opener.open({ kind: 'path', path: '~/.zshrc' }, origin)
    expect(d.viewed[0]).toMatchObject({ path: '/Users/a/.zshrc' })
  })

  it('says so when a relative path has no verified cwd to hang on', async () => {
    const d = deps()
    await createLinkOpener(d).open(
      { kind: 'path', path: 'docs/x.md' },
      { ...origin, cwd: null, cwdVerified: false },
    )
    expect(d.viewed).toEqual([])
    expect(d.said).toHaveLength(1)
    expect(d.said[0]).toContain('docs/x.md')
  })

  it('says so when ~ cannot be expanded', async () => {
    const d = deps({
      openBinding: () => Promise.resolve(openResult({ root: root('/etc', '/etc') })),
    })
    await createLinkOpener(d).open({ kind: 'path', path: '~/.zshrc' }, origin)
    expect(d.viewed).toEqual([])
    expect(d.said[0]).toContain('~/.zshrc')
  })

  it('says so when the binding cannot be opened at all', async () => {
    const d = deps({ openBinding: () => Promise.reject(new Error('session gone')) })
    await createLinkOpener(d).open({ kind: 'path', path: 'a.ts' }, origin)
    expect(d.viewed).toEqual([])
    expect(d.said).toHaveLength(1)
  })

  it('does not cache a binding that failed to open', async () => {
    let calls = 0
    const d = deps({
      openBinding: () => {
        calls++
        return calls === 1 ? Promise.reject(new Error('nope')) : Promise.resolve(openResult())
      },
    })
    const opener = createLinkOpener(d)
    await opener.open({ kind: 'path', path: 'a.ts' }, origin)
    await opener.open({ kind: 'path', path: 'a.ts' }, origin)
    expect(calls).toBe(2)
    expect(d.viewed).toHaveLength(1)
  })

  it('refuses a link printed in a tab with no origin at all', async () => {
    const d = deps()
    await createLinkOpener(d).open({ kind: 'path', path: 'a.ts' }, null)
    expect(d.viewed).toEqual([])
    expect(d.said).toHaveLength(1)
  })
})
