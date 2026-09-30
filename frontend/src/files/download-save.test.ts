// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'

import { downloadResultFixture } from './download-fixtures'
import { createBrowserDownloadSaver, createNativeDownloadSaver } from './download-save'

function recordingDocument(): { doc: Document; clicked: HTMLAnchorElement[] } {
  const clicked: HTMLAnchorElement[] = []
  const real = document.createElement.bind(document)
  const doc = {
    createElement(tag: string) {
      const el = real(tag)
      if (tag === 'a') (el as HTMLAnchorElement).click = () => clicked.push(el as HTMLAnchorElement)
      return el
    },
  } as unknown as Document
  return { doc, clicked }
}

describe('browser download preparation', () => {
  it('hands the resolved URL to a detached noopener anchor without fetching bytes', async () => {
    const { doc, clicked } = recordingDocument()
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)
    try {
      const prepared = await createBrowserDownloadSaver(doc).prepare('suggested.iso')
      if (prepared === null) throw new Error('browser saver unexpectedly cancelled')

      expect(prepared.destination).toBe('browser')
      await expect(
        prepared.save(downloadResultFixture(), 'http://127.0.0.1/download/a'),
      ).resolves.toBe('handed-off')
      expect(clicked).toHaveLength(1)
      expect(clicked[0].href).toBe('http://127.0.0.1/download/a')
      expect(clicked[0].download).toBe('')
      expect(clicked[0].rel).toBe('noopener')
      expect(document.body.contains(clicked[0])).toBe(false)
      expect(fetchSpy).not.toHaveBeenCalled()
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('reports a missing URL without creating an anchor', async () => {
    const { doc, clicked } = recordingDocument()
    const prepared = await createBrowserDownloadSaver(doc).prepare('a')
    if (prepared === null) throw new Error('browser saver unexpectedly cancelled')

    await expect(prepared.save(downloadResultFixture(), null)).resolves.toBe('destination-failed')
    expect(clicked).toEqual([])
  })
})
describe('native prepared downloads', () => {
  it('sends the opaque handle, ticket, and measured size and retires the handle', async () => {
    const calls: unknown[][] = []
    const saver = createNativeDownloadSaver({
      prepare(name) {
        calls.push(['prepare', name])
        return Promise.resolve('opaque-handle')
      },
      save(handle, ticket, size) {
        calls.push(['save', handle, ticket, size])
        return Promise.resolve({ outcome: 'saved' })
      },
      discard(handle) {
        calls.push(['discard', handle])
        return Promise.resolve()
      },
    })
    const prepared = await saver.prepare('suggested.iso')
    expect(prepared?.destination).toBe('native')
    await expect(
      prepared?.save(downloadResultFixture({ ticket: 'b'.repeat(64), size: 42 }), null),
    ).resolves.toBe('saved')
    expect(calls).toEqual([
      ['prepare', 'suggested.iso'],
      ['save', 'opaque-handle', 'b'.repeat(64), 42],
      ['discard', 'opaque-handle'],
    ])
    prepared?.dispose()
    expect(calls).toHaveLength(3)
  })

  it('discards a handle when save rejects before consumption', async () => {
    const discarded: string[] = []
    const prepared = await createNativeDownloadSaver({
      prepare() {
        return Promise.resolve('opaque-handle')
      },
      save() {
        return Promise.reject(new Error('save refused'))
      },
      discard(handle) {
        discarded.push(handle)
        return Promise.resolve()
      },
    }).prepare('a')
    await expect(prepared?.save(downloadResultFixture(), null)).rejects.toThrow('save refused')
    prepared?.dispose()
    expect(discarded).toEqual(['opaque-handle'])
  })

  it('allows cancellation to retire a running save exactly once', async () => {
    const discarded: string[] = []
    let resolveSave!: (value: { outcome: string }) => void
    const savePromise = new Promise<{ outcome: string }>((resolve) => {
      resolveSave = resolve
    })
    const prepared = await createNativeDownloadSaver({
      prepare() {
        return Promise.resolve('opaque-handle')
      },
      save() {
        return savePromise
      },
      discard(handle) {
        discarded.push(handle)
        return Promise.resolve()
      },
    }).prepare('a')
    if (prepared === null) throw new Error('expected a native prepared handle')
    const saving = prepared.save(downloadResultFixture(), null)
    prepared.cancel()
    resolveSave({ outcome: 'cancelled' })
    await expect(saving).resolves.toBe('cancelled')
    prepared.dispose()
    expect(discarded).toEqual(['opaque-handle'])
  })

  it('discards a prepared handle at most once when cancelled or disposed', async () => {
    const discarded: string[] = []
    const prepared = await createNativeDownloadSaver({
      prepare() {
        return Promise.resolve('opaque-handle')
      },
      save() {
        return Promise.resolve({ outcome: 'saved' })
      },
      discard(handle) {
        discarded.push(handle)
        return Promise.resolve()
      },
    }).prepare('a')
    prepared?.cancel()
    prepared?.dispose()
    expect(discarded).toEqual(['opaque-handle'])
  })
})
