// @vitest-environment jsdom
//
// The happy path, watched end to end with only the WIRE faked: a command's
// output is serialized the way a frozen block serializes it, decorated,
// ⌘-clicked, and a file-viewer target comes out the other side holding the
// absolute path and the line.
//
// Every module in the chain is the real one — the serializer, the grammar,
// the DOM decorator, the click surface, the resolver and the opener — which
// is the point. Each has its own unit tests and each passes them; what those
// cannot report is the seam between them, and this repo has shipped exactly
// that defect before (AGENTS.md, testing rule 1: every unit correct, the
// user's task impossible).
//
// It is also the check that the two SURFACES share one grammar. The live
// xterm policy and the frozen DOM decorator are exercised over the same row
// here, and asserted to find the same thing — a regression that gave either
// one its own regex would fail here and nowhere else.
import { describe, expect, it } from 'vitest'
import { serializeRange, DEFAULT_SNAPSHOT } from '../scrollback/serializer'
import { lineWith } from '../scrollback/test-helpers'
import { decorateLinks } from './decorate'
import { attachLinkClicks } from './surface'
import { createLinkOpener } from './open'
import type { LinkOpener } from './open'
import { createLivePolicy } from './live'
import { trackLinkModifier, type ArmedTracker } from './armed'
import type { FilesOpenResult } from '../generated/files.open'
import type { FileViewerTarget } from '../file-viewer'
import type { ActiveOrigin } from '../pane-content'

const ORIGIN: Omit<ActiveOrigin, 'paneId'> = {
  sessionId: 'sess-1',
  kind: 'local',
  cwd: '/Users/a/repo',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

const BINDING: FilesOpenResult = {
  bindingId: 'bind-1',
  endpointId: null,
  root: { path: '/Users/a/repo', display: '~/repo', inferred: false, inferredReason: '' },
  revealAvailable: true,
}

/** One row of output, through the SAME serializer a frozen block uses —
 *  half of it coloured, so the link straddles two colour runs exactly as it
 *  does when a program highlights part of a path. */
function frozenBlock(text: string, colourUntil = 0): HTMLElement {
  const line = lineWith(
    ...[...text].map((ch, i) => ({
      chars: ch,
      fg: i < colourUntil ? 1 : 7,
      bold: i < colourUntil,
    })),
  )
  const el = document.createElement('div')
  el.className = 'cmd-output'
  el.innerHTML = serializeRange(DEFAULT_SNAPSHOT, (y) => (y === 0 ? line : undefined), 0, 0)
  return el
}

function harness(): {
  opened: Array<FileViewerTarget & { line?: number }>
  urls: string[]
  directories: string[]
  armed: ArmedTracker
  opener: LinkOpener
} {
  const opened: Array<FileViewerTarget & { line?: number }> = []
  const urls: string[] = []
  const directories: string[] = []
  const opener = createLinkOpener({
    openUrl: (url) => {
      urls.push(url)
      return Promise.resolve()
    },
    openBinding: () => Promise.resolve(BINDING),
    pathKind: (_bindingId, path) =>
      Promise.resolve({
        kind: path === '/Users/a/repo/build' ? ('directory' as const) : ('file' as const),
      }),
    openDirectory: (path) => {
      directories.push(path)
      return Promise.resolve(true)
    },
    openViewer: (t) => opened.push(t),
    notify: () => {},
    onBindingLiveness: () => () => {},
  })
  return { opened, urls, directories, armed: trackLinkModifier(), opener }
}

function metaClick(el: Element): void {
  el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, metaKey: true }))
}

const flush = async (): Promise<void> => {
  await Promise.resolve()
  await Promise.resolve()
}

describe('a path printed by a command opens the file it names', () => {
  it('resolves against the tab’s cwd and carries the line', async () => {
    const block = frozenBlock('see docs/architecture.md:101 for the rest')
    document.body.append(block)
    decorateLinks(block)
    const { opened, armed, opener } = harness()
    const detach = attachLinkClicks(block, { opener, origin: () => ORIGIN, armed })

    const link = block.querySelector('a')
    expect(link?.textContent).toBe('docs/architecture.md:101')
    metaClick(link as Element)
    await flush()

    expect(opened).toHaveLength(1)
    expect(opened[0]).toMatchObject({
      bindingId: 'bind-1',
      path: '/Users/a/repo/docs/architecture.md',
      name: 'architecture.md',
      line: 101,
    })
    detach()
    armed.dispose()
  })

  it('reveals a directory through the Files panel and never opens its viewer', async () => {
    const block = frozenBlock('see /Users/a/repo/build for generated files')
    document.body.append(block)
    decorateLinks(block)
    const { opened, directories, armed, opener } = harness()
    const detach = attachLinkClicks(block, { opener, origin: () => ORIGIN, armed })

    metaClick(block.querySelector('a') as Element)
    await flush()

    expect(directories).toEqual(['/Users/a/repo/build'])
    expect(opened).toEqual([])
    detach()
    armed.dispose()
  })

  it('survives a path a program printed half-coloured', async () => {
    // The serializer emits one span per colour run; a link that spans two of
    // them is the case a node-local walk truncates.
    //
    // IT IS NOW SEVERAL ANCHORS, AND THAT IS THE POINT OF THE ASSERTION
    // BELOW (nocx-ec18). This used to read `querySelector('a').textContent`
    // and expect the whole of `AGENTS.md:84`, which stated that a link
    // straddling colour runs is ONE element. The decorator can no longer
    // promise that: wrapping the whole link means extracting one Range
    // across the row's nodes, and when an end of that Range falls inside a
    // `.term-cell` — the fixed-width box that keeps a frozen row on its
    // columns — the box is split and the row loses its geometry. Each text
    // node's slice is wrapped on its own instead.
    //
    // So what the user can do is stated twice: the link READS whole across
    // its parts, and clicking a part that is NOT the first still opens it.
    // The second half is the one that would have been silently lost — the
    // target has to be written to every anchor, not just the one the old
    // assertion happened to look at.
    const block = frozenBlock('AGENTS.md:84 is the contract', 6)
    document.body.append(block)
    decorateLinks(block)
    expect(block.querySelectorAll('span[style]').length).toBeGreaterThan(1)

    const { opened, armed, opener } = harness()
    const detach = attachLinkClicks(block, { opener, origin: () => ORIGIN, armed })
    const parts = [...block.querySelectorAll('a')]
    expect(parts.length).toBeGreaterThan(1)
    expect(parts.map((a) => a.textContent).join('')).toBe('AGENTS.md:84')
    metaClick(parts[parts.length - 1])
    await flush()
    expect(opened[0]).toMatchObject({ path: '/Users/a/repo/AGENTS.md', line: 84 })
    detach()
    armed.dispose()
  })

  it('opens a url through the browser seam, not the viewer', async () => {
    const block = frozenBlock('docs at https://example.com/guide')
    document.body.append(block)
    decorateLinks(block)
    const { opened, urls, armed, opener } = harness()
    const detach = attachLinkClicks(block, { opener, origin: () => ORIGIN, armed })
    metaClick(block.querySelector('a') as Element)
    await flush()
    expect(urls).toEqual(['https://example.com/guide'])
    expect(opened).toEqual([])
    detach()
    armed.dispose()
  })
})

describe('both surfaces read one grammar', () => {
  it('the live policy and the frozen decorator find the same spans', () => {
    const text = 'see docs/architecture.md:101 and https://example.com/x, not v0.3.0'
    const block = frozenBlock(text)
    decorateLinks(block)
    const fromDom = [...block.querySelectorAll('a')].map((a) => a.textContent)

    const { armed, opener } = harness()
    // The live policy answers only while armed — that IS the contract, so
    // arm it the way the modifier does rather than working around it.
    const policy = createLivePolicy({
      opener,
      origin: () => ORIGIN,
      armed: { armed: () => true, subscribe: () => () => {}, dispose: () => {} },
      notify: () => {},
    })
    const fromGrid = policy.ranges(text).map((r) => text.slice(r.from, r.to))

    expect(fromGrid).toEqual(fromDom)
    expect(fromGrid).toEqual(['docs/architecture.md:101', 'https://example.com/x'])
    armed.dispose()
  })
})
