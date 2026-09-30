// DownloadClient — the download half of the files.* control plane, and the
// one thing about a download that is not on the WebSocket at all: the GET
// that carries the bytes (contracts/files.download.schema.json).
//
// It is `upload-client.ts`'s mirror and deliberately not its extension. The
// three methods look alike and the shapes are different in ways a shared
// seam would have to paper over: a download's result is a single branch
// where an upload's is a three-way union, its outcome enum is
// sent/cancelled/failed where an upload's is written/skipped/cancelled/
// failed, and its terminal frame carries `bytes` where an upload's carries
// `finalName` and `stranded`. A `TransferServices` covering both would be
// a union of two vocabularies with a discriminator at every read.
//
// ## Bulk bytes stay off the WebSocket
//
// The result carries a one-shot HTTP ticket. Browser builds hand its URL to
// the browser for a streamed attachment; native builds pass the same URL to
// the local atomic receiver. Neither path buffers the body into a renderer
// Blob, and neither sends file bytes over JSON-RPC. The renderer's own
// lifecycle comes from `files.downloadProgress` and `files.downloadDone`.

import type { Dispatcher } from '../dispatcher'
import type { FilesDownloadResult } from '../generated/files.download'
import type { FilesDownloadCancelResult } from '../generated/files.downloadCancel'
import type { FilesDownloadDone } from '../generated/files.downloadDone'
import type { FilesDownloadProgress } from '../generated/files.downloadProgress'
import type { FilesDownloadCompleteResult } from '../generated/files.downloadComplete'

/** The renderer names the remote source, and native only when selected by
 *  the prepared receiver. It never supplies a local destination. */
export interface DownloadRequest {
  bindingId: string
  path: string
  destination?: 'native'
}

type NativeDownloadOutcome = 'saved' | 'cancelled' | 'source-failed' | 'destination-failed'

/** The download feature's entire backend surface, so a test can substitute
 *  a fake — the ports pattern the Files panel already uses. */
export interface DownloadServices {
  /** Mint one transfer: an id, a one-shot ticket, the URL that redeems it,
   *  the name it lands under and the size measured on the open handle. */
  download(req: DownloadRequest): Promise<FilesDownloadResult>
  cancel(transferId: string): Promise<FilesDownloadCancelResult>
  complete(transferId: string, outcome: NativeDownloadOutcome): Promise<FilesDownloadCompleteResult>
  /** Resolve the result's `url` — a PATH on the backend's HTTP surface —
   *  against the socket's own origin. It is here rather than in the saver
   *  because only the client holds the dispatcher, and it is the same
   *  resolution `UploadClient.sendBody` does: under `dev-web` vite serves
   *  the page and the backend is on another port, so the document's origin
   *  is not where the bytes are. Null when there is no connection, which
   *  the caller reports rather than guessing an origin. */
  resolveUrl(url: string): string | null
  /** files.downloadProgress — live and lossy; an indicator, never a
   *  ledger. */
  subscribeProgress(handler: (p: FilesDownloadProgress) => void): () => void
  /** files.downloadDone — retained per session and flushed on attach. */
  subscribeDone(handler: (p: FilesDownloadDone) => void): () => void
}

class DownloadClient {
  constructor(private dispatcher: Dispatcher) {}

  download(req: DownloadRequest): Promise<FilesDownloadResult> {
    return this.dispatcher.call<FilesDownloadResult>('files.download', {
      bindingId: req.bindingId,
      path: req.path,
      ...(req.destination === undefined ? {} : { destination: req.destination }),
    })
  }

  cancel(transferId: string): Promise<FilesDownloadCancelResult> {
    return this.dispatcher.call<FilesDownloadCancelResult>('files.downloadCancel', { transferId })
  }

  complete(
    transferId: string,
    outcome: NativeDownloadOutcome,
  ): Promise<FilesDownloadCompleteResult> {
    return this.dispatcher.call<FilesDownloadCompleteResult>('files.downloadComplete', {
      transferId,
      outcome,
    })
  }

  resolveUrl(url: string): string | null {
    const socket = this.dispatcher.socket
    if (socket === null) return null
    const base = new URL(socket.url)
    base.protocol = base.protocol === 'wss:' ? 'https:' : 'http:'
    return new URL(url, base.origin).toString()
  }

  subscribeProgress(handler: (p: FilesDownloadProgress) => void): () => void {
    return this.dispatcher.subscribe('files.downloadProgress', (params: unknown) => {
      const p = params as FilesDownloadProgress
      if (
        p !== null &&
        typeof p === 'object' &&
        typeof p.transferId === 'string' &&
        typeof p.bytes === 'number' &&
        typeof p.total === 'number'
      ) {
        handler(p)
      }
    })
  }

  subscribeDone(handler: (p: FilesDownloadDone) => void): () => void {
    return this.dispatcher.subscribe('files.downloadDone', (params: unknown) => {
      const p = params as FilesDownloadDone
      if (
        p !== null &&
        typeof p === 'object' &&
        typeof p.transferId === 'string' &&
        typeof p.outcome === 'string' &&
        typeof p.name === 'string' &&
        typeof p.bytes === 'number' &&
        typeof p.total === 'number'
      ) {
        handler(p)
      }
    })
  }
}

/** Real implementation over the dispatcher. */
export function createDownloadServices(dispatcher: Dispatcher): DownloadServices {
  const client = new DownloadClient(dispatcher)
  return {
    download: (req) => client.download(req),
    cancel: (transferId) => client.cancel(transferId),
    complete: (transferId, outcome) => client.complete(transferId, outcome),
    resolveUrl: (url) => client.resolveUrl(url),
    subscribeProgress: (handler) => client.subscribeProgress(handler),
    subscribeDone: (handler) => client.subscribeDone(handler),
  }
}
