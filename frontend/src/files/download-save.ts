// How a download reaches the disk — one seam with browser and native
// implementations, selected at the composition root.
//
// ## The renderer never names a destination path
//
// A browser receives the backend's attachment URL; a Wails window sends only
// the opaque transfer id to `files.downloadSave`. In neither case can the
// renderer choose or observe a local destination path. The browser picks its
// configured downloads directory; the native backend asks through the OS save
// dialog and keeps that answer behind the control-plane boundary.
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

interface DownloadSave {
  /** The only value native save sends over the wire. */
  transferId: string
  /** The authoritative name measured on the pinned source handle. */
  name: string
  /** Used only by the browser implementation; null while no socket origin is reachable. */
  url: string | null
}

/** The browser never claimed the backend ticket, so no authoritative done
 * frame can arrive until its TTL cancels it. The flow may mark that local
 * half failed immediately; native RPC failures are different because the
 * backend has already claimed and will send the terminal account. */
export class DownloadNotClaimedError extends Error {}

/** Hand one download to the selected platform. */
export interface DownloadSaver {
  save(download: DownloadSave): Promise<void>
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
    save(download: DownloadSave): Promise<void> {
      if (download.url === null) {
        return Promise.reject(
          new DownloadNotClaimedError(
            `${download.name}: there is no connection to the backend to fetch the bytes over`,
          ),
        )
      }
      const a = doc.createElement('a')
      a.href = download.url
      // Empty: "save it, and take the name from the response". A name here
      // would be the renderer overriding Content-Disposition.
      a.download = ''
      a.rel = 'noopener'
      a.click()
      return Promise.resolve()
    },
  }
}

/** The desktop save starts and ends entirely behind the control plane. */
export function createNativeDownloadSaver(
  saveNative: (transferId: string) => Promise<unknown>,
): DownloadSaver {
  return {
    async save(download: DownloadSave): Promise<void> {
      await saveNative(download.transferId)
    },
  }
}
