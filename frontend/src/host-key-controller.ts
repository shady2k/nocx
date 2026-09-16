import type { HelperConsentAskEvidence, HostKeyErrorEvidence } from './terminal-content'

interface QueuedAsk<Evidence> {
  evidence: Evidence
  signal: AbortSignal
  resolve: (accepted: boolean) => void
  abort: () => void
  settled: boolean
}

/**
 * Serialises one kind of open-time decision without losing the requesting
 * tab. Generic over the evidence a request carries, so the host-key ask and
 * the connect-time helper ask (ADR-0068) share one queueing mechanism
 * rather than each inventing its own (AD-8) — they differ only in what
 * counts as "the same question" (sameAsk), which OpenHostKeyRequestQueue and
 * HelperConsentAskQueue below each supply once.
 */
class AskQueue<Evidence> {
  private readonly waiting: QueuedAsk<Evidence>[] = []
  private activeRequest: QueuedAsk<Evidence> | null = null

  constructor(
    private readonly onActiveChange: (request: QueuedAsk<Evidence> | null) => void,
    private readonly sameAsk: (a: Evidence, b: Evidence) => boolean,
  ) {}

  request(evidence: Evidence, signal: AbortSignal): Promise<boolean> {
    // Promise.withResolvers needs ES2024 and this project targets ES2021, so
    // the resolver is captured via the executor form.
    let resolve!: (accepted: boolean) => void
    const promise = new Promise<boolean>((done) => {
      resolve = done
    })
    if (signal.aborted) {
      resolve(false)
      return promise
    }
    const request: QueuedAsk<Evidence> = {
      evidence,
      signal,
      resolve,
      settled: false,
      abort: () => {},
    }
    request.abort = () => this.settle(request, false)
    signal.addEventListener('abort', request.abort, { once: true })
    this.waiting.push(request)
    this.showNext()
    return promise
  }

  settle(request: QueuedAsk<Evidence>, accepted: boolean): void {
    if (request.settled) return
    request.settled = true
    request.signal.removeEventListener('abort', request.abort)
    if (this.activeRequest === request) {
      this.activeRequest = null
      this.onActiveChange(null)
      request.resolve(accepted)
      this.showNext()
      return
    }
    const queued = this.waiting.indexOf(request)
    if (queued >= 0) this.waiting.splice(queued, 1)
    request.resolve(accepted)
  }

  /** One successful answer answers every queued tab asking the same question. */
  settleMatchingQueued(acceptedRequest: QueuedAsk<Evidence>): void {
    for (const request of [...this.waiting]) {
      if (this.sameAsk(request.evidence, acceptedRequest.evidence)) {
        this.settle(request, true)
      }
    }
  }

  private showNext(): void {
    if (this.activeRequest || this.waiting.length === 0) return
    this.activeRequest = this.waiting.shift() ?? null
    this.onActiveChange(this.activeRequest)
  }
}

export type OpenHostKeyRequest = QueuedAsk<HostKeyErrorEvidence>

/** Serialises open-time host-key decisions without losing the requesting tab. */
export class OpenHostKeyRequestQueue extends AskQueue<HostKeyErrorEvidence> {
  constructor(onActiveChange: (request: OpenHostKeyRequest | null) => void) {
    // Two requests are the same question when they name the same backend
    // storage identity and offer the same key — matching connections.test's
    // own hostKey evidence, which is what a duplicate open racing this one
    // would have received too.
    super(onActiveChange, (a, b) => a.knownHostsHost === b.knownHostsHost && a.key === b.key)
  }
}

export type HelperConsentAskRequest = QueuedAsk<HelperConsentAskEvidence>

/**
 * Serialises the connect-time helper ask (ADR-0068) without losing the
 * requesting tab: two panes opening auto connections to the SAME machine at
 * once must not raise the dialog twice, and answering one answers both —
 * the fingerprint is the identity ADR-0034 already keys the answer by, so
 * it is what "the same question" means here.
 */
export class HelperConsentAskQueue extends AskQueue<HelperConsentAskEvidence> {
  constructor(onActiveChange: (request: HelperConsentAskRequest | null) => void) {
    super(onActiveChange, (a, b) => a.fingerprint === b.fingerprint)
  }
}
