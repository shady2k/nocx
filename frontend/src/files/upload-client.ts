// UploadClient — the upload half of the files.* control plane (design §5.3),
// plus the one thing that is not on the WebSocket at all: the POST that
// carries the bytes (§5.4, ADR for D3).
//
// One seam, `UploadServices`, and both gestures consume it — the terminal
// drop and the Files panel's action. That is deliberate: a test substitutes
// ONE object to drive the whole feature, and the two surfaces cannot drift
// apart by each holding a different half of the wire.
//
// dialog.openFileForUpload lands here rather than on DialogClient beside
// dialog.openFile, and the reason is what the contract says about it: it is
// a SIBLING of the picker, not the picker — dialog.openFile answers with a
// path and this one never does, because the renderer may name an upload's
// destination and never its source (R2). It has exactly one consumer, the
// upload flow, and it is meaningless without files.upload to echo its
// ticket into. Splitting it off would make each surface depend on two
// clients for one gesture.

import type { Dispatcher } from '../dispatcher'
import type { DialogOpenFileForUpload } from '../generated/dialog.openFileForUpload'
import type { FilesDropped } from '../generated/files.dropped'
import type { FilesUploadResult } from '../generated/files.upload'
import type { FilesUploadCancelResult } from '../generated/files.uploadCancel'
import type { FilesUploadDone } from '../generated/files.uploadDone'
import type { FilesUploadProgress } from '../generated/files.uploadProgress'

/** The person's collision decision, in the wire's words. The kit dialog
 *  answers in exactly these three, so the flow echoes rather than maps —
 *  a mapping table between two spellings of one closed set is a second
 *  owner of the vocabulary. */
export type UploadDecision = 'overwrite' | 'keepBoth' | 'skip'

/** The whole of what a renderer may say about an upload (§5.3). There is no
 *  sourcePath and no source discriminator: a request carrying a sourceTicket
 *  is a path upload, one without is a stream upload, and what the renderer
 *  cannot spell it cannot ask for (R2). */
export interface UploadRequest {
  bindingId: string
  destDir: string
  name: string
  size: number
  /** Backend-minted, echoed never authored. Absent for a stream upload. */
  sourceTicket?: string
  /** Absent until the person has been asked — the backend answers
   *  `collision:"exists"` rather than deciding on their behalf (D5). */
  onExists?: UploadDecision
}

/**
 * What became of the one-shot POST that carries the bytes.
 *
 * A STATUS and a NETWORK failure are different answers to different
 * questions and the caller must be able to tell them apart: `409` means the
 * ticket was already claimed and somebody else's body is running (the
 * transfer is alive and not ours to mourn), `410` means the ticket names
 * nothing — never minted, expired, or the transfer already ended — and a
 * network failure means the request never got an answer at all, so the
 * transfer's fate is unknown until files.uploadDone says. Collapsing these
 * into one `Error` is how "the upload failed" gets shown for a transfer
 * that is about to succeed.
 */
export type SendBodyOutcome =
  | { ok: true }
  | { ok: false; kind: 'status'; status: number }
  | { ok: false; kind: 'network'; message: string }
  /** The body disagrees with the size declared at mint time. The sink would
   *  answer 400 before reading a byte (§5.4); refusing here says the same
   *  thing without moving the file across the socket first. */
  | { ok: false; kind: 'size'; declared: number; actual: number }

/** The upload feature's entire backend surface, so a test can substitute a
 *  fake — the ports pattern the Files panel already uses. */
export interface UploadServices {
  /** Start (or ask about) one transfer. Three outcomes, exactly one branch
   *  matching: a collision nobody has decided about, a running transfer that
   *  needs no body, or a sink waiting for one. */
  upload(req: UploadRequest): Promise<FilesUploadResult>
  /** Idempotent: cancelling a finished transfer is not an error, because
   *  the person's cancel races the transfer's own completion every time. */
  cancel(transferId: string): Promise<FilesUploadCancelResult>
  /** POST the bytes to the url files.upload returned. */
  sendBody(url: string, body: Blob, size: number, signal?: AbortSignal): Promise<SendBodyOutcome>
  /** files.uploadProgress — live and lossy; an indicator, never a ledger. */
  subscribeProgress(handler: (p: FilesUploadProgress) => void): () => void
  /** files.uploadDone — retained per session and flushed on attach. */
  subscribeDone(handler: (p: FilesUploadDone) => void): () => void
  /** files.dropped — the Wails window drop, already minted into tickets. */
  subscribeDropped(handler: (p: FilesDropped) => void): () => void
  /** The native picker used as an upload SOURCE. Rejects with -32601 where
   *  there is no Wails; the caller degrades to the browser's own picker
   *  rather than inventing a way to name a path. */
  pickSource(): Promise<DialogOpenFileForUpload>
}

/** The `fetch` the body rides on, named so a test can pass its own. */
export type FetchLike = (input: string, init: RequestInit) => Promise<Response>

class UploadClient {
  constructor(
    private dispatcher: Dispatcher,
    private fetchImpl: FetchLike,
  ) {}

  upload(req: UploadRequest): Promise<FilesUploadResult> {
    // Built field by field rather than spread: `sourceTicket: undefined`
    // marshals to a key the strict decoder refuses, and "a request with a
    // sourceTicket is a path upload" keys on the key's PRESENCE.
    const params: Record<string, unknown> = {
      bindingId: req.bindingId,
      destDir: req.destDir,
      name: req.name,
      size: req.size,
    }
    if (req.sourceTicket !== undefined) params.sourceTicket = req.sourceTicket
    if (req.onExists !== undefined) params.onExists = req.onExists
    return this.dispatcher.call<FilesUploadResult>('files.upload', params)
  }

  cancel(transferId: string): Promise<FilesUploadCancelResult> {
    return this.dispatcher.call<FilesUploadCancelResult>('files.uploadCancel', { transferId })
  }

  /**
   * The one request in the renderer that is not JSON-RPC (AD-1's amendment,
   * D3): a streamed POST on the backend's own HTTP surface, because the data
   * plane carries PTY I/O and a multi-gigabyte upload multiplexed onto it
   * would compete with terminal responsiveness.
   *
   * **Content-Length is not ours to set.** The sink requires it and refuses
   * a body whose length disagrees with the size declared at mint time, and
   * the temptation is to declare it here. `Content-Length` is a FORBIDDEN
   * header: a browser silently drops an attempt to set one, so the header
   * that reached the server was always the browser's own, computed from the
   * blob. Setting it was therefore theatre in every real client and a lie in
   * the one test that asserted it. What makes the requirement hold is the
   * check below — the blob is refused before the request when its own `size`
   * is not the declared one — which is what makes the value the browser
   * computes the declared size too.
   *
   * The route is cross-origin in every browser configuration this ships in,
   * so the backend answers a preflight and names this origin back on every
   * reply (`internal/transport/ws_upload.go`). Nothing here asks for that;
   * it is stated because a reply that stops naming the origin turns every
   * status below into an unreadable "Failed to fetch".
   *
   * The url the result carried is a PATH on the backend's HTTP surface. It
   * is resolved against the socket's own origin rather than the document's:
   * `dev-web` is served by vite on a different port, and the page's origin
   * is not where the backend is.
   */
  async sendBody(
    url: string,
    body: Blob,
    size: number,
    signal?: AbortSignal,
  ): Promise<SendBodyOutcome> {
    if (body.size !== size) {
      return { ok: false, kind: 'size', declared: size, actual: body.size }
    }
    const socket = this.dispatcher.socket
    if (socket === null) {
      return { ok: false, kind: 'network', message: 'no connection to the backend' }
    }
    const base = new URL(socket.url)
    base.protocol = base.protocol === 'wss:' ? 'https:' : 'http:'
    const target = new URL(url, base.origin).toString()
    let res: Response
    try {
      res = await this.fetchImpl(target, {
        method: 'POST',
        body,
        signal,
      })
    } catch (e) {
      return { ok: false, kind: 'network', message: e instanceof Error ? e.message : String(e) }
    }
    if (!res.ok) return { ok: false, kind: 'status', status: res.status }
    return { ok: true }
  }

  subscribeProgress(handler: (p: FilesUploadProgress) => void): () => void {
    return this.dispatcher.subscribe('files.uploadProgress', (params: unknown) => {
      const p = params as FilesUploadProgress
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

  subscribeDone(handler: (p: FilesUploadDone) => void): () => void {
    return this.dispatcher.subscribe('files.uploadDone', (params: unknown) => {
      const p = params as FilesUploadDone
      if (
        p !== null &&
        typeof p === 'object' &&
        typeof p.transferId === 'string' &&
        typeof p.outcome === 'string' &&
        Array.isArray(p.stranded)
      ) {
        handler(p)
      }
    })
  }

  subscribeDropped(handler: (p: FilesDropped) => void): () => void {
    return this.dispatcher.subscribe('files.dropped', (params: unknown) => {
      const p = params as FilesDropped
      if (
        p !== null &&
        typeof p === 'object' &&
        typeof p.sessionId === 'string' &&
        Array.isArray(p.sources)
      ) {
        handler(p)
      }
    })
  }

  pickSource(): Promise<DialogOpenFileForUpload> {
    return this.dispatcher.call<DialogOpenFileForUpload>('dialog.openFileForUpload', {})
  }
}

/** Real implementation over the dispatcher. `fetchImpl` defaults to the
 *  platform's, and is a parameter so the body path is testable without a
 *  server — the same reason the panel's services are an interface. */
export function createUploadServices(
  dispatcher: Dispatcher,
  fetchImpl: FetchLike = (input, init) => fetch(input, init),
): UploadServices {
  const client = new UploadClient(dispatcher, fetchImpl)
  return {
    upload: (req) => client.upload(req),
    cancel: (transferId) => client.cancel(transferId),
    sendBody: (url, body, size, signal) => client.sendBody(url, body, size, signal),
    subscribeProgress: (handler) => client.subscribeProgress(handler),
    subscribeDone: (handler) => client.subscribeDone(handler),
    subscribeDropped: (handler) => client.subscribeDropped(handler),
    pickSource: () => client.pickSource(),
  }
}
