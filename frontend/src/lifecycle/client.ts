// LifecycleClient — the renderer's subscription seam for the lifecycle.changed
// notification (ADR-0024 decision 7; contracts/lifecycle.changed.schema.json).
//
// Authentication terminates in the backend. This client never sees a raw
// channel frame, a capability, or a sequence counter — only the schema-checked
// published fact (internal/lifecyclepub), which is exactly what the kernel
// concluded. The renderer validates legal application transitions and can
// construct no authority of its own; this file is the thin consumer that makes
// the generated type reachable end to end, and the two-axis state machine
// (ADR-0024 decision 6) builds on the facts it exposes.
//
// The wire shape is guarded at the boundary like files.changed and git.changed
// (the same unsolicited-notification defect class): a payload without a string
// sessionId/lane pair is not a fact, and a session receives only its own facts.

import type { Dispatcher } from '../dispatcher'
import type { LifecycleChanged } from '../generated/lifecycle.changed'
import type { LifecycleFact } from './state'
import type { LifecycleRecoverAck } from '../generated/lifecycle.recoverAck'
import type { LifecycleEstablishAck } from '../generated/lifecycle.establishAck'
import type { LifecycleSubmitAttempt } from '../generated/lifecycle.submitAttempt'

/** One lifecycle fact after routing by its server-authoritative session id.
 *  The lane then lets that session's projection attach the fact to the right
 *  state machine. */
export type LifecycleFactHandler = (fact: LifecycleFact) => void
/** A lifecycle subscription is installed before session.open starts, then
 *  bound exactly once to the server-authoritative session id from its result.
 *  Before that binding it delivers nothing: the backend installs the session's
 *  subscriber only after the open result, so a fact for this session cannot
 *  exist yet, and a fact for another session is not ours to deliver. */
export interface LifecycleChangedSubscription {
  bindSession: (sessionId: string) => void
  unsubscribe: () => void
}

/** The payload of lifecycle.submitAttempt: the app-owned half of a command's
 *  execution, declared before the bytes that can cause the shell's own start
 *  are written to the pty (ADR-0024 decision 5). The command text is the
 *  reference-intact record line — never the resolved send line. */
export interface LifecycleSubmitAttemptParams {
  /** The live domain to open the attempt on — the id the published
   *  prompt_ready fact carried. */
  readonly domain: string
  /** The app-owned command text as submitted. */
  readonly command: string
  /** The cwd the command runs in, captured at submit. */
  readonly cwd: string
  /** The host the command runs on, captured at submit. */
  readonly host: string
  /** WHO submitted it, in the LEDGER's vocabulary ('user' is the person at
   *  the keyboard, 'assistant' is the agent's lane) — the same mapping
   *  history-client.ts makes at the wire, because the two calls write one
   *  column and must speak one language.
   *
   *  Required, with no default. The durable row is OPENED by this call
   *  (nocx-kpqr3), so this is where the submitting target's author reaches
   *  the store; the later history.record moves the status and leaves the
   *  column alone. A default here would let a submit path forget it and
   *  quietly attribute the assistant's command to the person — which is
   *  exactly what a restored pane then showed (nocx-1druc). */
  readonly source: 'user' | 'assistant'
}

export class LifecycleClient {
  constructor(private dispatcher: Dispatcher) {}

  /** Subscribe before session.open so the open result and the replay that
   *  immediately follows it cannot overtake registration, then bind the
   *  server-authoritative id the result carries. Binding filters before any
   *  surface can mutate or acknowledge state.
   *
   *  There is deliberately no pre-bind buffer. Catching up on a fact
   *  published while open was still dialing has ONE owner and it is the
   *  backend: handleOpen installs the session's subscriber only after the
   *  open result, PublishLifecycle drops a fact that has no subscriber, and
   *  replayLifecycleFacts re-emits the current projection the instant that
   *  subscriber lands. A fact for this session therefore cannot arrive
   *  before bindSession, and one for another session belongs to that
   *  session's own subscription — so a buffer here could only ever hold
   *  facts it must then discard. */
  subscribeLifecycleChanged(handler: LifecycleFactHandler): LifecycleChangedSubscription {
    let sessionId: string | null = null
    let closed = false
    const unsubscribe = this.dispatcher.subscribe('lifecycle.changed', (params: unknown) => {
      const p = params as LifecycleChanged
      if (!p || typeof p.sessionId !== 'string' || typeof p.lane !== 'string') return
      if (p.sessionId === sessionId) handler(p)
    })
    return {
      bindSession: (authoritativeSessionId: string): void => {
        if (closed) return
        if (sessionId !== null) throw new Error('lifecycle subscription is already bound')
        sessionId = authoritativeSessionId
      },
      unsubscribe: (): void => {
        if (closed) return
        closed = true
        unsubscribe()
      },
    }
  }

  /** Open an app-originated attempt on the live domain — the ordering seam
   *  of ADR-0024 decision 5. The renderer calls this BEFORE writing the
   *  command bytes to the pty; the later authenticated start attaches to the
   *  returned attempt and replaces nothing. Resolves with the attempt as the
   *  kernel created it. */
  submitAttempt(params: LifecycleSubmitAttemptParams): Promise<LifecycleSubmitAttempt> {
    return this.dispatcher.call('lifecycle.submitAttempt', params)
  }

  /** Acknowledge a restoration (ADR-0024 decision 8): the renderer matched
   *  the shell's one-shot recovery fence AND applied the conventional
   *  presentation, so the lane may fall Lost → Native. The params are
   *  deliberately narrow — session identity and the recovery generation the
   *  lost fact carried; nothing else. The backend accepts only while the
   *  session is recovery-pending and alive, and the transition permits only
   *  Lost → Native. */
  recoverAck(sessionId: string, generation: string): Promise<LifecycleRecoverAck> {
    return this.dispatcher.call('lifecycle.recoverAck', { sessionId, generation })
  }

  /** Acknowledge an establishment (ADR-0024 decision 9): the renderer has
   *  processed the published prompt_ready fact for the exact {lane, domain,
   *  epoch, generation} and committed the presentation that makes an editor
   *  available. The backend flushes the pending accept only on this
   *  acknowledgement — no acknowledgement, no accept, and the shell's
   *  bounded handshake wait expires with its native prompt visible
   *  (fail-open). The params are narrow: session identity, the lane/domain/
   *  epoch addressing tuple and the backend-minted generation the fact
   *  carried. The call is fire-and-forget from the renderer: a refusal (a
   *  stale generation, a superseded establishment) is the backend's own
   *  bookkeeping, and the session stays conventional. */
  establishAck(
    sessionId: string,
    lane: string,
    domain: string,
    epoch: number,
    generation: string,
  ): Promise<LifecycleEstablishAck> {
    return this.dispatcher.call('lifecycle.establishAck', {
      sessionId,
      lane,
      domain,
      epoch,
      generation,
    })
  }
}
