// The renderer's half of "the checkout sweep cannot run, and here is why"
// (nocx-xn63t.1.6).
//
// The failure: with the content store unavailable, the durable record of
// nocx-made checkouts is not wired, the sweep can judge nothing and remove
// nothing — while Settings went on offering "Remove unused worker checkouts
// after" governing nothing. A silent degrade the UI contradicts is how a
// feature that does not exist survives a release (AGENTS.md).
//
// The sentences live here and nowhere else, keyed on the closed `reason`
// enum rather than on the backend's prose, so a reworded Go error never
// changes what a user reads — the same rule history-status.ts states for
// its own surface. There is deliberately no notification subscription: the
// noRecord degrade is decided before the transport starts and never lifts;
// recordWrites is raised at runtime when the record refuses a write and is
// equally sticky. The store re-reads on every reconnect and the section
// reads it when it renders, which is how a runtime raise arrives.

import type { WSClient } from './ipc'

// The wire type is GENERATED from contracts/checkouts.status.schema.json
// (npm run contracts) and re-exported here so callers import it from the
// module that speaks checkouts.status. Do not re-declare it — change the
// schema (contracts/README.md).
export type { CheckoutsStatus } from './generated/checkouts.status'
import type { CheckoutsStatus } from './generated/checkouts.status'

/** The two lines of the notice: what is true, and why. */
export interface CheckoutsUnavailableSentence {
  title: string
  description: string
}

/**
 * What to say about a status, or null when there is nothing to say.
 *
 * `null` status means "not read yet" and is deliberately NOT a degrade: a
 * surface must show its placeholder rather than a lie in either direction
 * (the rule agent-status-line.ts states for the assistant's credential).
 */
export function checkoutsUnavailableSentence(
  status: CheckoutsStatus | null,
): CheckoutsUnavailableSentence | null {
  if (status === null || status.available) return null
  let description: string
  switch (status.reason) {
    case 'noRecord':
      description =
        'The store that remembers nocx-made checkouts could not be opened, so no checkout is judged and none is ever removed.'
      break
    case 'recordWrites':
      description =
        'The record of nocx-made checkouts refused a write, so the last-used times it holds cannot be trusted and nothing is aged out until the backend restarts.'
      break
    default:
      // The enum is closed on the wire, and this is the honest fallback if a
      // newer backend names a reason this build does not know: say the fact
      // without inventing a why.
      description = 'The sweep cannot run, and this build does not know why.'
  }
  const detail = status.detail ? ` (${status.detail})` : ''
  return {
    title: 'Unused worker checkouts are not being cleaned up',
    description: description + detail,
  }
}
/** Mirrors the backend's answer for the sections that render it. Reads once
 *  on start and again on every reconnect — there is no notification to
 *  subscribe to; a degrade raised after a client attached (recordWrites) is
 *  read on the next refresh, which the section's next render drives. */
export class CheckoutsStatusStore {
  private current: CheckoutsStatus | null = null
  private readonly listeners = new Set<(s: CheckoutsStatus | null) => void>()
  private readonly client: WSClient
  private started = false

  constructor(client: WSClient) {
    this.client = client
  }

  /** The status as last read, or null until the first read answers. */
  status(): CheckoutsStatus | null {
    return this.current
  }

  /** Observe the status. Returns an unsubscribe. */
  subscribe(listener: (s: CheckoutsStatus | null) => void): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  private set(next: CheckoutsStatus | null): void {
    const same =
      this.current !== null &&
      next !== null &&
      this.current.available === next.available &&
      this.current.reason === next.reason &&
      this.current.detail === next.detail
    if (same) return
    this.current = next
    for (const listener of this.listeners) listener(next)
  }

  start(): void {
    if (this.started) return
    this.started = true
    // On connect, not only once: the backend's status is in memory, so a
    // restarted backend has a status of its own and the mirror must be
    // replaced rather than kept.
    this.client.dispatcher.onConnect(() => {
      if (!this.started) return
      void this.refresh()
    })
    void this.refresh()
  }

  /** Read the status now. A failed read leaves the mirror alone: an
   *  unanswered question is not an answer, and claiming a degrade because a
   *  socket hiccuped is the same class of lie as hiding one. */
  async refresh(): Promise<void> {
    try {
      const status = await this.client.call<CheckoutsStatus>('checkouts.status', {})
      this.set(status)
    } catch {
      // The mirror keeps whatever it last read.
    }
  }
}
