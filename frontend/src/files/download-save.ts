// How a download reaches the disk — one seam, one implementation, and one
// thing it deliberately does not do.
//
// ## The renderer never names a destination path
//
// There is no path parameter here and there is nowhere for one to go. The
// backend answers `files.download` with a URL and
// `Content-Disposition: attachment`, so handing that URL to the browser is
// the whole of the save: the browser puts the file where that person's
// browser puts files, under the name the header carries, by the platform's
// own mechanism. A renderer that chose a directory would be inventing an
// answer the person never gave it, and the one place a person DOES get
// asked — the desktop build's native save dialog — is a backend method
// (nocx-9le.8.4) that will arrive as another implementation of this
// interface, not as a path threaded through this one.
//
// ## Why an anchor and not `fetch` into a Blob
//
// The obvious shape is `fetch(url) -> res.blob() -> URL.createObjectURL`,
// and it is wrong for the reason the contract gives for putting these bytes
// on HTTP in the first place: "a browser streams an HTTP response to disk
// by itself, where a page receiving WebSocket messages would have to hold
// the whole file in the renderer's heap before any of it reached the disk."
// A Blob puts it back in the heap. The download the owner asked for is the
// one for files too big to read in the terminal, so the shape that dies on
// a 4 GB file is the shape that fails the feature.
//
// A navigation costs the renderer nothing and buys nothing back either: it
// cannot watch the transfer, and it does not need to. Progress arrives as
// `files.downloadProgress` and the terminal account as
// `files.downloadDone`, which is RETAINED against the session — so the row
// is correct across a reload the browser's own download survives too.
// Cancel reaches the backend, which stops writing the body; the browser
// then sees a body short of the Content-Length it was promised and reports
// the download as failed, which is what it is.
//
// The one-shot ticket is spent by that navigation and by nothing else, so
// the saver is called exactly once per transfer and never retried: a second
// attempt on a spent ticket is a 410, which would report a failure for a
// file that is already on the disk.

/** Hand one download to the platform. The URL is absolute — the client
 *  resolved it against the socket's origin, because under `dev-web` the
 *  page and the backend are not on the same one. */
export interface DownloadSaver {
  /** Start the save. Returns nothing: whether the bytes arrive is the
   *  transfer's account (files.downloadDone), not this call's, and a
   *  boolean here would be a second opinion about it. */
  save(url: string): void
}

/**
 * The browser's own download, started by clicking a detached anchor.
 *
 * `document.body` is not touched: a click on an element outside the
 * document still dispatches, and appending one — even for a tick — is a
 * visible mutation of somebody's page for no gain.
 *
 * `download` is set but is not what makes this work. On a cross-origin URL
 * the attribute is ignored by every browser, and under `dev-web` this URL
 * is always cross-origin; `Content-Disposition: attachment` is what forces
 * the save and names the file, and the backend sets it on every reply. The
 * attribute stays for the same-origin case (the packaged app, where the
 * page is served by the backend), where it is the difference between a save
 * and a navigation to a binary the browser then has to guess about.
 *
 * `rel="noopener"` because an anchor click that DOES navigate must not hand
 * the opened context a handle on this one.
 */
export function createBrowserDownloadSaver(doc: Document = document): DownloadSaver {
  return {
    save(url: string): void {
      const a = doc.createElement('a')
      a.href = url
      // Empty: "save it, and take the name from the response". A name here
      // would be the renderer overriding Content-Disposition, which is the
      // sanitised one and the only one that has seen the real bytes.
      a.download = ''
      a.rel = 'noopener'
      a.click()
    },
  }
}
