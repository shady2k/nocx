/**
 * The renderer's ONE owner of "can the assistant answer, and with what"
 * (nocx-rikz5, AD-8).
 *
 * Before this there was no readiness STATE in the renderer at all:
 * terminal-content.ts passed `status: () => new AgentClient(…).status()`
 * into the ask target — a function called at refusal time, not a stored
 * fact. A function cannot repaint anything, so a chip built on it would be
 * correct once and then stop moving. This module is the stored fact: one
 * store, one `refresh()`, one subscribe seam. Every surface reads it;
 * nothing else calls `agent.status`.
 *
 * The ordering rule is the other half. Two refreshes racing is the ORDINARY
 * case here — adding an endpoint and immediately choosing a model is one
 * gesture apart — so each refresh carries a monotonically increasing
 * sequence and a reply at or below the last APPLIED one is discarded. The
 * alternative (cancel the in-flight call) is not available: a JSON-RPC call
 * cannot be recalled, and "ignore what came back" is the same guarantee at
 * the only end we control.
 */
import type { AgentStatusResult } from './generated/agent.status'
import { agentStatusLine } from './agent-status-line'

/** What the chip in the composer's chrome row says right now (nocx-rikz5).
 *  'ready' names the pair that will answer — two chips, two destinations.
 *  'action' is the rung of the ladder the person is on, in the ladder's own
 *  words; `page` is null for a rung no page repairs. */
export type ModelChipState =
  | { kind: 'ready'; endpoint: string; model: string }
  | { kind: 'action'; text: string; page: 'endpoints' | 'roles' | null }

/** The one call this store makes. An interface rather than the concrete
 *  AgentClient so a surface test can hand it a source without a socket —
 *  and so this module does not reach for a dispatcher it has no other use
 *  for. */
export interface AgentStatusSource {
  status(): Promise<AgentStatusResult>
}

export class AgentReadiness {
  /** The last APPLIED answer. Null until the first refresh answers — which
   *  is "nothing has been read yet", never "not ready": a surface shows its
   *  placeholder rather than a lie. */
  private _status: AgentStatusResult | null = null
  /** Sequence of the last refresh ISSUED, and of the last one APPLIED.
   *  Two numbers, not one: the gap between them is exactly the set of
   *  replies still in flight, and a reply is applied only if it closes the
   *  gap forwards. */
  private _issued = 0
  private _applied = 0
  private readonly listeners = new Set<(status: AgentStatusResult | null) => void>()

  constructor(private readonly source: AgentStatusSource) {}

  get status(): AgentStatusResult | null {
    return this._status
  }

  /** Every surface that renders readiness subscribes here rather than
   *  calling `agent.status` itself. Returns the unsubscribe. */
  subscribe(listener: (status: AgentStatusResult | null) => void): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  /**
   * Ask the backend and adopt the answer when it is the newest one seen.
   *
   * Rejects when the read fails, and leaves the stored fact ALONE when it
   * does: a socket that could not answer is not a fact about the assistant,
   * so the chip goes on saying what was last true rather than going blank.
   * The caller decides what a failure costs it — the ask target's refusal
   * path wants to know; a fire-and-forget refresh does not.
   */
  async refresh(): Promise<AgentStatusResult> {
    const seq = ++this._issued
    const status = await this.source.status()
    if (seq <= this._applied) return status
    this._applied = seq
    this._status = status
    for (const listener of [...this.listeners]) listener(status)
    return status
  }
}

/**
 * The chip's words, derived from the status and NEVER invented here: an
 * unresolved role's sentence and its repair page come from `agentStatusLine`
 * — the one owner of "status → readiness sentence" (AD-8). Two surfaces
 * wording one rung differently is how one state says different things in two
 * places.
 *
 * null means no status has been read yet: no chip, rather than a chip
 * claiming something.
 */
export function modelChipState(status: AgentStatusResult | null): ModelChipState | null {
  if (!status) return null
  const answering = status.answering
  // The pair, when there is one. The null checks are not defensive noise:
  // the contract says both are non-null when `ready`, and a chip is the
  // wrong place to discover otherwise — falling through to the rung shows
  // the person a sentence rather than the word "null".
  if (answering.ready && answering.endpoint !== null && answering.model !== null) {
    return { kind: 'ready', endpoint: answering.endpoint, model: answering.model }
  }
  const line = agentStatusLine(status)
  if (!line) return null
  return { kind: 'action', text: line.text, page: line.fix?.page ?? null }
}
