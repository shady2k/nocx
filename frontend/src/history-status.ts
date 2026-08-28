// The renderer's half of "durable command history is not running, and here
// is why" (nocx-rtg0.15).
//
// The bug: failing soft was right — a terminal that will not start because
// its history key would not open is worse than one that starts without
// history — but the soft failure was a slog.Warn and nothing else, while
// Settings went on offering a keep-history toggle, a retention age and a
// two-number budget that governed nothing.
//
// ── One mechanism, not two ────────────────────────────────────────────────
//
// Two different unavailabilities speak through this module and must never
// grow two vocabularies: the store never opened (this bead), and the store
// is open but writes are failing or the outbox overflowed at runtime
// (nocx-rtg0.10, whose policy already says it raises through THIS surface,
// once per degrade episode rather than once per lost command, with the
// interval closed when the queue drains). Hence the raise/clear shape rather
// than a one-shot toast, and hence a name that says what is true rather than
// when it became true. A runtime failure arrives as another `reason` and
// another sentence below — it does not get a second store, a second notice
// or a second wire method. If you are about to add one, this is the reason
// not to.
//
// The sentences live here and nowhere else, for the same reason `source` and
// HasRows live in one place each: two surfaces wording one fact go out of
// step at the moment the wording matters.

// Framework-neutral on purpose, like settings-domain.ts: no DOM, no Solid.
// Two surfaces read this — the Solid Settings screen and the vanilla recall
// panel — so a store that only one of them can hold would force the other to
// grow its own copy of the fact, which is the defect this module exists to
// prevent.
import type { WSClient } from './ipc'

// The wire type is GENERATED from contracts/history.status.schema.json
// (npm run contracts) and re-exported here so callers import it from the
// module that speaks history.status. Do not re-declare it — change the
// schema (contracts/README.md).
export type { HistoryStatus } from './generated/history.status'
import type { HistoryStatus } from './generated/history.status'

/** The two lines of the notice: what is true, and why. */
export interface HistoryUnavailableSentence {
  title: string
  description: string
}

/**
 * What to say about a status, or null when there is nothing to say.
 *
 * Keyed on the closed `reason` enum rather than on the backend's prose, so a
 * reworded Go error never changes what a user reads. `detail` is appended
 * when there is one: the reason says which part of the machinery is down,
 * the detail is what a bug report needs.
 *
 * `null` status means "not read yet" and is deliberately NOT a degrade: a
 * surface must show its placeholder rather than a lie in either direction
 * (the rule agent-status-line.ts states for the assistant's credential).
 */
export function historyUnavailableSentence(
  status: HistoryStatus | null,
): HistoryUnavailableSentence | null {
  if (status === null || status.available) return null
  let description: string
  switch (status.reason) {
    case 'noKey':
      description =
        'The key that encrypts the history database could not be read, so nothing is being stored.'
      break
    case 'invalidBudget':
      description =
        'The size limits below could not be applied, so the store was not opened and nothing is being stored.'
      break
    case 'openFailed':
      description = 'The history database could not be opened, so nothing is being stored.'
      break
    case 'writeFailed':
      // The only reason that can end without a restart, and the sentence says
      // so: the person can stop reading and come back to a working feature.
      description =
        'The history database is refusing writes, so commands are running but not being kept.'
      break
    default:
      // A reason this build does not know — a newer backend, or a degrade
      // raised without one. Saying less is still honest; saying nothing
      // would put the settings back in charge of a feature that is down.
      description = 'Nothing is being stored.'
  }
  if (status.detail !== null && status.detail !== '') {
    description += ` (${status.detail})`
  }
  return {
    // The state, in the terms the settings under it are written in: those
    // controls all say "keep", so this says what is not being kept.
    title: 'Commands are not being kept',
    description,
  }
}

/**
 * What to say about a DISCARD, or null when there was none.
 *
 * A DIFFERENT FACT FROM THE ONE ABOVE, and kept apart on purpose: history is
 * running, and it is empty because the storage format changed under it
 * (nocx-rtg0.19). The sentence above says a feature is down; this one says a
 * working feature starts from nothing. Folding them together would make the
 * settings below read as ungoverned when they govern perfectly.
 *
 * It is worth saying at all because the symptom is invisible: an empty
 * history after an update looks exactly like a fresh install, and a person
 * with no explanation concludes the feature never worked.
 *
 * -1 is the store's "there was something and I could not count it", which is
 * still a discard and still theirs to know.
 */
export function historyDiscardSentence(
  status: HistoryStatus | null,
): HistoryUnavailableSentence | null {
  if (status === null || status.discarded === null || status.discarded === undefined) return null
  const rows = status.discarded
  return {
    title: 'Earlier history was discarded',
    description:
      rows < 0
        ? 'The storage format changed in this version, so what was kept before could not be carried over.'
        : `The storage format changed in this version, so ${rows === 1 ? '1 command' : `${rows} commands`} kept before it could not be carried over.`,
  }
}

/**
 * What to say about a DETACHED session's output, or null when it is being
 * kept (nocx-22k1c.1).
 *
 * A THIRD FACT, kept apart from the two above for the reason they are kept
 * apart from each other. The first says durable history is down; the second
 * says a working history starts from nothing; this one says a session whose
 * window is closed will STOP — which is a statement about the terminal, not
 * about the history panel, and the only place the person can learn it is
 * beside the switch that caused it.
 *
 * The mechanism, in one line: the backend buffers a detached session's output
 * and frees that buffer either when a client acknowledges the bytes or when
 * the recorder writes them down. With no client and no recorder there is
 * nobody to free it, so the session throttles once it fills. That is the
 * deliberate choice — nothing is ever dropped — and it is exactly why it has
 * to be said out loud.
 */
export function detachedOutputSentence(
  status: HistoryStatus | null,
): HistoryUnavailableSentence | null {
  if (status === null || status.detachedOutput.recorded) return null
  const cause =
    status.detachedOutput.reason === 'outputOff'
      ? 'Command output is not being kept'
      : 'History is turned off'
  return {
    // Said in the terms the person is in: they are looking at switches about
    // keeping things, and this is the one consequence of those switches that
    // is not about keeping things at all.
    title: 'Sessions keep running only while a window is open',
    description:
      `${cause}, so there is nowhere to write what a session prints while its window is closed. ` +
      'Such a session pauses once its output buffer fills and continues when a window is opened on it again. ' +
      'Nothing it printed is lost.',
  }
}

/**
 * What the recall panel puts in its empty list when there is no store to
 * answer from — the third state of `source`, distinct from "the store
 * answered and had nothing".
 *
 * Separate wording from the Settings sentence because the reader is in a
 * different place doing a different thing: in Settings they are looking at
 * the controls this contradicts, in recall they pressed Up expecting their
 * history. Same fact, one owner, two audiences.
 */
export const HISTORY_UNAVAILABLE_RECALL_TITLE = 'history is not being kept'
export const HISTORY_UNAVAILABLE_RECALL_DESC = 'see Settings → History for why'

/**
 * The status, kept current.
 *
 * Reads history.status once on start and on every reconnect, and listens for
 * history.statusChanged in between — the raise/clear push, which is how a
 * degrade that begins after startup reaches a screen already on the History
 * section. Modelled on VaultObserver, with one deliberate difference: the
 * notification carries the whole status, and this store uses it, because
 * unlike a vault snapshot there is nothing further to fetch and a refetch
 * would put a round trip between the degrade and the sentence about it.
 */
export class HistoryStatusStore {
  private current: HistoryStatus | null = null
  private readonly listeners = new Set<(s: HistoryStatus | null) => void>()
  private unsub: (() => void) | null = null
  private unsubConnect: (() => void) | null = null
  private started = false

  constructor(private readonly client: WSClient) {}

  /** The status as last read, or null until the first read answers. */
  status(): HistoryStatus | null {
    return this.current
  }

  /** Observe the status. Fires on every change, never on a re-read that
   *  changed nothing — a surface re-rendering the same sentence is noise a
   *  raise/clear shape exists to avoid. Returns an unsubscribe. */
  subscribe(listener: (s: HistoryStatus | null) => void): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  private set(next: HistoryStatus | null): void {
    if (sameStatus(this.current, next)) return
    this.current = next
    for (const listener of this.listeners) listener(next)
  }

  start(): void {
    if (this.started) return
    this.started = true
    this.unsub = this.client.dispatcher.subscribe('history.statusChanged', (params: unknown) => {
      if (!this.started) return
      this.set(asHistoryStatus(params))
    })
    // On connect, not only once: the backend's status is in memory, so a
    // restarted backend has a status of its own and the mirror must be
    // replaced rather than kept.
    this.unsubConnect = this.client.dispatcher.onConnect(() => {
      if (!this.started) return
      void this.refresh()
    })
    void this.refresh()
  }

  stop(): void {
    this.started = false
    this.unsub?.()
    this.unsub = null
    this.unsubConnect?.()
    this.unsubConnect = null
  }

  /** Read the status now. A failed read leaves the mirror alone: an
   *  unanswered question is not an answer, and claiming a degrade because a
   *  socket hiccuped is the same class of lie as hiding one. */
  async refresh(): Promise<void> {
    try {
      const status = await this.client.call<HistoryStatus>('history.status', {})
      this.set(asHistoryStatus(status))
    } catch {
      /* keep the last known status */
    }
  }
}

/** Whether two statuses say the same thing. Field-wise rather than by
 *  identity: every read mints a fresh object, so identity would report a
 *  change on every reconnect. */
function sameStatus(a: HistoryStatus | null, b: HistoryStatus | null): boolean {
  if (a === null || b === null) return a === b
  return (
    a.available === b.available &&
    a.reason === b.reason &&
    a.detail === b.detail &&
    a.discarded === b.discarded &&
    a.detachedOutput.recorded === b.detachedOutput.recorded &&
    a.detachedOutput.reason === b.detachedOutput.reason
  )
}

/** Narrow an untrusted notification payload to the wire type. A malformed
 *  push is dropped rather than rendered: null is "not read yet", which is a
 *  state every reader already handles. */
function asHistoryStatus(params: unknown): HistoryStatus | null {
  if (typeof params !== 'object' || params === null) return null
  const p = params as Record<string, unknown>
  if (typeof p.available !== 'boolean') return null
  const reason = p.reason
  const detail = p.detail
  const discarded = p.discarded
  return {
    available: p.available,
    reason: typeof reason === 'string' ? (reason as HistoryStatus['reason']) : null,
    detail: typeof detail === 'string' ? detail : null,
    // A non-integer is dropped to null rather than coerced: "how many
    // commands you lost" is a number or it is nothing, and a NaN rendered
    // into that sentence would be worse than not saying it.
    discarded: typeof discarded === 'number' && Number.isInteger(discarded) ? discarded : null,
    detachedOutput: asDetachedOutput(p.detachedOutput),
  }
}

/** Narrow the detached-output half of an untrusted payload.
 *
 *  A malformed or absent one reads as "being recorded", which is the quiet
 *  answer: this surface exists to WARN, and a warning invented from a
 *  payload nobody could parse would put a sentence about a stopped session in
 *  front of somebody whose sessions are fine. The push is dropped whole a few
 *  lines above when it is not an object at all; this is the field-level
 *  version of the same rule. */
function asDetachedOutput(raw: unknown): HistoryStatus['detachedOutput'] {
  if (typeof raw !== 'object' || raw === null) return { recorded: true, reason: null }
  const d = raw as Record<string, unknown>
  if (typeof d.recorded !== 'boolean') return { recorded: true, reason: null }
  const reason = d.reason
  return {
    recorded: d.recorded,
    reason:
      typeof reason === 'string' ? (reason as HistoryStatus['detachedOutput']['reason']) : null,
  }
}
