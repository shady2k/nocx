import type { IntegrationMethod } from './host-key-dialog'
import type { HelperConsentAskEvidence, HostKeyErrorEvidence } from './terminal-content'

interface QueuedAsk<Evidence, Answer> {
  evidence: Evidence
  signal: AbortSignal
  resolve: (answer: Answer) => void
  abort: () => void
  settled: boolean
}

/**
 * Serialises one kind of open-time decision without losing the requesting
 * tab. Generic over the evidence a request carries AND the answer it
 * resolves with, so the host-key ask (a plain accept/decline) and the
 * connect-time ask (ADR-0069: a method, or nothing) share one queueing
 * mechanism rather than each inventing its own (AD-8) — they differ only in
 * what counts as "the same question" (sameAsk) and what a decline resolves
 * to (declineValue), which OpenHostKeyRequestQueue and HelperConsentAskQueue
 * below each supply once.
 */
class AskQueue<Evidence, Answer> {
  private readonly waiting: QueuedAsk<Evidence, Answer>[] = []
  private activeRequest: QueuedAsk<Evidence, Answer> | null = null

  constructor(
    private readonly onActiveChange: (request: QueuedAsk<Evidence, Answer> | null) => void,
    private readonly sameAsk: (a: Evidence, b: Evidence) => boolean,
    private readonly declineValue: Answer,
  ) {}

  request(evidence: Evidence, signal: AbortSignal): Promise<Answer> {
    // Promise.withResolvers needs ES2024 and this project targets ES2021, so
    // the resolver is captured via the executor form.
    let resolve!: (answer: Answer) => void
    const promise = new Promise<Answer>((done) => {
      resolve = done
    })
    if (signal.aborted) {
      resolve(this.declineValue)
      return promise
    }
    const request: QueuedAsk<Evidence, Answer> = {
      evidence,
      signal,
      resolve,
      settled: false,
      abort: () => {},
    }
    request.abort = () => this.settle(request, this.declineValue)
    signal.addEventListener('abort', request.abort, { once: true })
    this.waiting.push(request)
    this.showNext()
    return promise
  }

  settle(request: QueuedAsk<Evidence, Answer>, answer: Answer): void {
    if (request.settled) return
    request.settled = true
    request.signal.removeEventListener('abort', request.abort)
    if (this.activeRequest === request) {
      this.activeRequest = null
      this.onActiveChange(null)
      request.resolve(answer)
      this.showNext()
      return
    }
    const queued = this.waiting.indexOf(request)
    if (queued >= 0) this.waiting.splice(queued, 1)
    request.resolve(answer)
  }

  /** One successful answer answers every queued tab asking the same question. */
  settleMatchingQueued(acceptedRequest: QueuedAsk<Evidence, Answer>, answer: Answer): void {
    for (const request of [...this.waiting]) {
      if (this.sameAsk(request.evidence, acceptedRequest.evidence)) {
        this.settle(request, answer)
      }
    }
  }

  private showNext(): void {
    if (this.activeRequest || this.waiting.length === 0) return
    this.activeRequest = this.waiting.shift() ?? null
    this.onActiveChange(this.activeRequest)
  }
}

export type OpenHostKeyRequest = QueuedAsk<HostKeyErrorEvidence, boolean>

/** Serialises open-time host-key decisions without losing the requesting tab. */
export class OpenHostKeyRequestQueue extends AskQueue<HostKeyErrorEvidence, boolean> {
  constructor(onActiveChange: (request: OpenHostKeyRequest | null) => void) {
    // Two requests are the same question when they name the same backend
    // storage identity and offer the same key — matching connections.test's
    // own hostKey evidence, which is what a duplicate open racing this one
    // would have received too.
    super(onActiveChange, (a, b) => a.knownHostsHost === b.knownHostsHost && a.key === b.key, false)
  }
}

export type HelperConsentAskRequest = QueuedAsk<HelperConsentAskEvidence, IntegrationMethod | null>

/**
 * Serialises the connect-time integration-method ask (ADR-0069) without
 * losing the requesting tab: two panes opening auto connections to the SAME
 * machine at once must not raise the dialog twice, and answering one answers
 * both — the fingerprint is the identity ADR-0034 already keys the consent
 * grant by, so it is what "the same question" means here. A decline (Cancel,
 * or trusting only the host key) resolves null — there is no method to
 * retry with, so the caller's open fails and the next connect asks again.
 */
export class HelperConsentAskQueue extends AskQueue<
  HelperConsentAskEvidence,
  IntegrationMethod | null
> {
  constructor(onActiveChange: (request: HelperConsentAskRequest | null) => void) {
    super(onActiveChange, (a, b) => a.fingerprint === b.fingerprint, null)
  }
}
