// @vitest-environment jsdom
//
// The saver, and the two things it must not do.
//
// It must not fetch the bytes into a Blob — the whole reason these bytes
// ride HTTP is that the browser streams them to disk without the renderer
// holding them, and a Blob puts a 4 GB file back in the heap. And it must
// not name a destination: the renderer has no path to choose and the
// backend's Content-Disposition is the only name that has seen the real
// bytes.
import { describe, expect, it, vi } from 'vitest'

import { createBrowserDownloadSaver } from './download-save'

/** A document whose anchors record their click instead of navigating —
 *  jsdom cannot follow one, and a test must not want it to. */
function recordingDocument(): { doc: Document; clicked: HTMLAnchorElement[] } {
  const clicked: HTMLAnchorElement[] = []
  const real = document.createElement.bind(document)
  const doc = {
    createElement(tag: string) {
      const el = real(tag)
      if (tag === 'a') {
        const a = el as HTMLAnchorElement
        a.click = () => clicked.push(a)
      }
      return el
    },
  } as unknown as Document
  return { doc, clicked }
}

describe('handing a download to the browser', () => {
  it('clicks an anchor at the URL, once', () => {
    const { doc, clicked } = recordingDocument()
    createBrowserDownloadSaver(doc).save('http://127.0.0.1:7331/download/abc')

    expect(clicked).toHaveLength(1)
    expect(clicked[0].href).toBe('http://127.0.0.1:7331/download/abc')
  })

  it('names no file — the response does', () => {
    // An empty `download` says "save it, and take the name from the
    // response". A name here would override Content-Disposition, which is
    // the sanitised one and the only one the backend built from the handle
    // it actually read.
    const { doc, clicked } = recordingDocument()
    createBrowserDownloadSaver(doc).save('http://127.0.0.1:7331/download/abc')

    expect(clicked[0].getAttribute('download')).toBe('')
  })

  it('never fetches the bytes into the renderer', async () => {
    // The regression this guards is a 4 GB file read into the heap before
    // any of it reaches the disk — the exact shape the contract rejects
    // when it explains why these bytes are on HTTP at all.
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)
    try {
      const { doc } = recordingDocument()
      createBrowserDownloadSaver(doc).save('http://127.0.0.1:7331/download/abc')
      // A microtask turn, so an implementation that fetched without
      // awaiting would still have called it.
      await Promise.resolve()
      expect(fetchSpy).not.toHaveBeenCalled()
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('leaves the document alone — the anchor is never appended', () => {
    // Appending even for a tick is a visible mutation of somebody's page,
    // and a click on a detached element dispatches perfectly well.
    const before = document.body.childNodes.length
    createBrowserDownloadSaver(recordingDocument().doc).save('http://x/download/abc')
    expect(document.body.childNodes.length).toBe(before)
  })
})
