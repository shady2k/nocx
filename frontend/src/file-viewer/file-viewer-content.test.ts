// @vitest-environment jsdom
//
// FileViewerContent tests — the rendered states, the dead-binding contract
// (content stays, no calls), and the read-only guarantee. The binding is a
// controllable fake: liveness is a real subscription with a synchronous first
// call, and every readFile call is recorded so "makes no client calls" is
// asserted as a count, not inferred from the absence of a message.
import { afterEach, describe, expect, it } from 'vitest'
import type { FilesReadResult } from '../generated/files.read'
import type { PaneHost } from '../pane-content'
import {
  FileViewerContent,
  type FileViewerDeps,
  type FileViewerTarget,
} from './file-viewer-content'

// ── Fake binding ──────────────────────────────────────────────────────────

class FakeBinding {
  live = true
  readonly calls: Array<{ bindingId: string; path: string }> = []
  private readonly cbs = new Set<(live: boolean) => void>()
  private readonly pending: Array<{
    resolve: (r: FilesReadResult) => void
    reject: (e: unknown) => void
  }> = []

  /** The deps object handed to the content; calls route back here. */
  readonly deps: FileViewerDeps = {
    readFile: (params) => {
      this.calls.push(params)
      return new Promise<FilesReadResult>((resolve, reject) => {
        this.pending.push({ resolve, reject })
      })
    },
    onBindingLiveness: (_bindingId, cb) => {
      this.cbs.add(cb)
      cb(this.live) // synchronous first call, like the real seam
      return () => {
        this.cbs.delete(cb)
      }
    },
  }

  setLive(live: boolean): void {
    this.live = live
    for (const cb of [...this.cbs]) cb(live)
  }

  /** Take the next pending read. Returns null when none is outstanding. */
  take(): { resolve: (r: FilesReadResult) => void; reject: (e: unknown) => void } | null {
    return this.pending.shift() ?? null
  }

  resolveNext(result: Partial<FilesReadResult>): void {
    const p = this.take()
    if (!p) throw new Error('no pending read to resolve')
    p.resolve(okResult(result))
  }

  rejectNext(error: Error): void {
    const p = this.take()
    if (!p) throw new Error('no pending read to reject')
    p.reject(error)
  }
}

function okResult(overrides: Partial<FilesReadResult>): FilesReadResult {
  return {
    path: '/srv/etc/nginx.conf',
    canonical: '/srv/etc/nginx.conf',
    text: 'hello\nworld\n',
    size: 13,
    modTime: '2026-08-06T00:00:00Z',
    truncated: false,
    binary: false,
    lossy: false,
    changed: false,
    ...overrides,
  }
}

const TARGET: FileViewerTarget = {
  bindingId: 'b1',
  endpointId: 'ep1',
  path: '/srv/etc/nginx.conf',
  canonical: '/srv/etc/nginx.conf',
  displayHost: 'srv-01',
  name: 'nginx.conf',
  origin: {
    sessionId: 'sess-1',
    kind: 'ssh',
    cwd: '/srv/etc',
    cwdVerified: false,
    // A viewer has no opinion about where we are — the frozen origin
    // must not drive the panel's reveal (brief §4).
    cwdFollow: false,
    host: 'srv-01',
  },
}

// ── Mount helpers ─────────────────────────────────────────────────────────

interface Mounted {
  content: FileViewerContent
  binding: FakeBinding
  host: HTMLElement
}

// CM6 renders each line as a div.cm-line (no newline text nodes), so a raw
// textContent read collapses lines. Joining the line divs reconstructs the
// document exactly, including a trailing empty line for a final newline.
const docText = (host: HTMLElement): string =>
  Array.from(host.querySelectorAll('.cm-line'))
    .map((el) => el.textContent ?? '')
    .join('\n')
async function mount(binding: FakeBinding = new FakeBinding()): Promise<Mounted> {
  const content = new FileViewerContent(TARGET, binding.deps)
  const host = document.createElement('div')
  document.body.append(host)
  const signal = new AbortController().signal
  await content.mount(host, {} as PaneHost, signal)
  return { content, binding, host }
}

const line = (host: HTMLElement, state: string): HTMLElement | null =>
  host.querySelector(`.file-viewer__line[data-state='${state}']`)

const reloadButton = (host: HTMLElement): HTMLButtonElement | null =>
  host.querySelector('.file-viewer__reload button')

const notice = (host: HTMLElement): HTMLElement =>
  host.querySelector('.file-viewer__notice') as HTMLElement

const clickReload = async (host: HTMLElement): Promise<void> => {
  reloadButton(host)!.click()
  await Promise.resolve()
  await Promise.resolve()
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('FileViewerContent — the read', () => {
  it('reads once on a live binding and renders the content', async () => {
    const { content, binding, host } = await mount()
    expect(binding.calls).toEqual([{ bindingId: 'b1', path: '/srv/etc/nginx.conf' }])

    binding.resolveNext({})
    await Promise.resolve()

    expect(docText(host)).toBe('hello\nworld\n')
    expect(notice(host).hidden).toBe(true)
    content.dispose()
  })

  it('renders nothing and makes no calls when the binding is dead at mount', async () => {
    const binding = new FakeBinding()
    binding.live = false
    const { content, host } = await mount(binding)

    expect(binding.calls).toEqual([])
    expect(line(host, 'unavailable')?.textContent).toContain('Source unavailable')
    expect(docText(host)).toBe('')
    content.dispose()
  })
})

describe('FileViewerContent — the activeOrigin capability (design §5.4)', () => {
  it('answers the origin the viewer was opened with, minus the paneId', async () => {
    const { content } = await mount()
    expect(content.activeOrigin()).toEqual({
      sessionId: 'sess-1',
      kind: 'ssh',
      cwd: '/srv/etc',
      cwdVerified: false,
      // A viewer has no opinion about where we are: the frozen origin
      // must never drive the panel's reveal (brief §4).
      cwdFollow: false,
      host: 'srv-01',
    })
    content.dispose()
  })

  it('answers null when the opener had no origin to hand over', async () => {
    const { content } = await mount()
    content.dispose()
    const bare = new FileViewerContent({ ...TARGET, origin: null }, new FakeBinding().deps)
    expect(bare.activeOrigin()).toBeNull()
    bare.dispose()
  })
})

describe('FileViewerContent — states that are not the file', () => {
  it('binary renders "binary file, N bytes" with nothing to show', async () => {
    const { content, binding, host } = await mount()
    binding.resolveNext({ binary: true, text: '', size: 42 })
    await Promise.resolve()

    expect(line(host, 'binary')?.textContent).toBe('binary file, 42 bytes')
    expect(docText(host)).toBe('')
    // No reload offer: the file did not change, it IS binary.
    expect(reloadButton(host)).toBeNull()
    content.dispose()
  })

  it('truncated says the ceiling was hit, with the size', async () => {
    const { content, binding, host } = await mount()
    binding.resolveNext({ truncated: true, text: 'prefix', size: 3 * 1024 * 1024 })
    await Promise.resolve()

    const t = line(host, 'truncated')
    expect(t?.textContent).toContain('2 MiB')
    expect(t?.textContent).toContain('3145728')
    expect(docText(host)).toBe('prefix')
    content.dispose()
  })

  it('lossy admits the view is not byte-identical', async () => {
    const { content, binding, host } = await mount()
    binding.resolveNext({ lossy: true, text: 'replaced\uFFFDchars' })
    await Promise.resolve()

    const l = line(host, 'lossy')
    expect(l?.textContent).toContain('not byte-identical')
    expect(docText(host)).toBe('replaced\uFFFDchars')
    content.dispose()
  })

  it('changed renders the mixture warning and offers Reload', async () => {
    const { content, binding, host } = await mount()
    binding.resolveNext({ changed: true, text: 'half-written' })
    await Promise.resolve()

    expect(line(host, 'changed')?.textContent).toContain('File changed')
    expect(reloadButton(host)?.disabled).toBe(false)
    expect(docText(host)).toBe('half-written')

    // Reload is an offer, and it is the only re-read: a clean second read
    // clears the notice.
    await clickReload(host)
    expect(binding.calls).toHaveLength(2)
    binding.resolveNext({ changed: false, text: 'full-content' })
    await Promise.resolve()

    expect(notice(host).hidden).toBe(true)
    expect(docText(host)).toBe('full-content')
    content.dispose()
  })

  it('a failed read on a live binding is an error state with a retry', async () => {
    const { content, binding, host } = await mount()
    binding.rejectNext(new Error('permission denied'))
    await Promise.resolve()

    const e = line(host, 'error')
    expect(e?.textContent).toContain('permission denied')
    expect(reloadButton(host)?.disabled).toBe(false)

    await clickReload(host)
    expect(binding.calls).toHaveLength(2)
    binding.resolveNext({ text: 'retried-ok' })
    await Promise.resolve()
    expect(docText(host)).toBe('retried-ok')
    expect(notice(host).hidden).toBe(true)
    content.dispose()
  })
})

describe('FileViewerContent — a dead binding is terminal for calls', () => {
  it('keeps the content, shows the unavailable state, and issues no further calls', async () => {
    const { content, binding, host } = await mount()
    binding.resolveNext({ text: 'still-on-screen' })
    await Promise.resolve()
    expect(docText(host)).toBe('still-on-screen')

    binding.setLive(false)
    await Promise.resolve()

    // Content stays; the banner says why; nothing new was called.
    expect(docText(host)).toBe('still-on-screen')
    expect(line(host, 'unavailable')?.textContent).toContain('Source unavailable')
    expect(binding.calls).toHaveLength(1)

    // Reload is present but disabled, and clicking it cannot call.
    const btn = reloadButton(host)
    expect(btn?.disabled).toBe(true)
    expect(btn?.title).toContain('gone')
    btn!.click()
    await Promise.resolve()
    expect(binding.calls).toHaveLength(1)
    content.dispose()
  })

  it('drops an in-flight read when the binding dies before it resolves', async () => {
    const { content, binding, host } = await mount()
    binding.setLive(false)
    // The pending read from mount resolves AFTER the death: it must not paint.
    binding.resolveNext({ text: 'late-content' })
    await Promise.resolve()

    expect(docText(host)).toBe('')
    expect(line(host, 'unavailable')).not.toBeNull()
    expect(binding.calls).toHaveLength(1)
    content.dispose()
  })

  it('re-enables Reload when the binding comes back, without auto-reloading', async () => {
    const { content, binding, host } = await mount()
    binding.resolveNext({ text: 'kept' })
    await Promise.resolve()
    binding.setLive(false)
    await Promise.resolve()

    binding.setLive(true)
    await Promise.resolve()

    // Still the stale content, still the banner — but the offer is live again
    // and the user's click is the only thing that reads (D6, D7).
    expect(docText(host)).toBe('kept')
    expect(line(host, 'unavailable')).not.toBeNull()
    expect(reloadButton(host)?.disabled).toBe(false)
    expect(binding.calls).toHaveLength(1)

    await clickReload(host)
    expect(binding.calls).toHaveLength(2)
    binding.resolveNext({ text: 'fresh' })
    await Promise.resolve()
    expect(docText(host)).toBe('fresh')
    expect(notice(host).hidden).toBe(true)
    content.dispose()
  })

  it('disposal drops an in-flight read and removes the DOM', async () => {
    const { content, binding, host } = await mount()
    binding.resolveNext({ text: 'late' })
    content.dispose()

    await Promise.resolve()
    expect(host.querySelector('.file-viewer')).toBeNull()
    // No exception from the late resolution, and nothing painted.
  })
})

describe('FileViewerContent — read-only', () => {
  it('no keystroke can reach the document', async () => {
    const { content, binding, host } = await mount()
    binding.resolveNext({ text: 'frozen\ncontent\n' })
    await Promise.resolve()

    const contentEl = host.querySelector('.cm-content') as HTMLElement
    // The structural guarantees: not an editable region, declared read-only.
    expect(contentEl.getAttribute('contenteditable')).toBe('false')
    expect(contentEl.getAttribute('aria-readonly')).toBe('true')

    const key = (init: KeyboardEventInit): void => {
      contentEl.dispatchEvent(
        new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
      )
    }
    key({ key: 'a' })
    key({ key: 'Enter' })
    key({ key: 'Backspace' })
    key({ key: 'x', ctrlKey: true })
    key({ key: 'z', ctrlKey: true }) // undo history is not even installed

    expect(docText(host)).toBe('frozen\ncontent\n')
    content.dispose()
  })
})

// ── Opening at a line (terminal links) ────────────────────────────────────
//
// A path printed as `docs/architecture.md:101` opens the file AT 101, and
// the line stays marked once the scroll has happened — otherwise the user
// arrives somewhere in a file and has to find the line again by eye, which
// is the work the link was supposed to save.

describe('FileViewerContent — opening at a line', () => {
  async function mountAt(line: number | undefined, text: string): Promise<Mounted> {
    const binding = new FakeBinding()
    const content = new FileViewerContent(
      line === undefined ? TARGET : { ...TARGET, line },
      binding.deps,
    )
    const host = document.createElement('div')
    document.body.append(host)
    await content.mount(host, {} as PaneHost, new AbortController().signal)
    binding.resolveNext({ text })
    await Promise.resolve()
    await Promise.resolve()
    return { content, binding, host }
  }

  it('marks the requested line', async () => {
    const { host, content } = await mountAt(3, 'one\ntwo\nthree\nfour\nfive\n')
    expect(host.querySelector('.cm-activeLine')?.textContent).toBe('three')
    content.dispose()
  })

  it('marks nothing when no line was requested', async () => {
    const { host, content } = await mountAt(undefined, 'one\ntwo\nthree\n')
    expect(host.querySelector('.cm-activeLine')).toBeNull()
    content.dispose()
  })

  it('clamps a line past the end of a file that has since shrunk', async () => {
    const { host, content } = await mountAt(999, 'one\ntwo\n')
    // Trailing newline gives a final empty line; the clamp lands on it
    // rather than refusing to open the file at all.
    expect(host.querySelector('.cm-activeLine')).not.toBeNull()
    content.dispose()
  })

  it('clamps a line of zero rather than throwing', async () => {
    const { host, content } = await mountAt(0, 'one\ntwo\n')
    expect(host.querySelector('.cm-activeLine')?.textContent).toBe('one')
    content.dispose()
  })
})
