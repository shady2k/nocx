// Call operations — the Operations source for a person's call that is still
// waiting (ADR-8eej3e66 §8).
//
// A row exists only while it means something: a person's call past two
// seconds, with the catalogue's waiting sentence and Cancel. Ordinary work
// that answers inside the threshold never appears at all, which is the owner's
// condition against a list that fills with every case.
//
// A MUTATION'S ROW IS KEPT, BECAUSE THE QUESTION IS (ADR-8eej3e66 §8). When a
// call that may have changed something ends without an answer — the person
// cancelled it, the socket went, the surface gave up — the row stops being a
// wait and becomes the answer: "did not start", "done", or "checking what
// happened". It never becomes a second attempt. A read's row still goes the
// moment it settles: there is no effect whose fate anybody needs.
//
// Background calls never reach this source: the dispatcher announces only
// person-origin operation calls, and background work merges on its key and
// stays in the log and diagnostics.

import { createSignal } from 'solid-js'
import type { Dispatcher, PendingCallView } from '../dispatcher'
import { callOperationDeclaration } from '../generated/operations'
import { rendererDeadlineMs } from './renderer-deadlines'
import { waitingSentence } from './waiting-sentences'
import type { Operation, OperationSource } from './operations'

/** Waiting under two seconds shows the action's own pending state. Two
 *  seconds is the owner's answer (ADR-8eej3e66 §8). */
const WAIT_ROW_THRESHOLD_MS = 2000

export interface CallOperations {
  /** The Operations source, read by the model. */
  source: OperationSource
  /** Stop observing. */
  dispose: () => void
}

interface WaitingCall {
  call: PendingCallView
  timer: ReturnType<typeof setTimeout> | null
  timerFired: boolean
  /** The renderer deadline projection: turn + max plus the margin, armed for
   *  every migrated domain (renderer-deadlines.ts). */
  deadlineMs: number | null
}

export function callOperations(
  dispatcher: Dispatcher,
  thresholdMs: number = WAIT_ROW_THRESHOLD_MS,
): CallOperations {
  const [rows, setRows] = createSignal<Operation[]>([])
  const waiting = new Map<number, WaitingCall>()

  const remove = (id: number): void => {
    const entry = waiting.get(id)
    if (entry === undefined) return
    if (entry.timer !== null) clearTimeout(entry.timer)
    waiting.delete(id)
    setRows((previous) => previous.filter((row) => row.id !== callRowID(id)))
  }

  const onPending = (call: PendingCallView): void => {
    const declaration = callOperationDeclaration(call.method)
    // A method outside the catalogue, or one the catalogue says never shows,
    // takes no row. The call itself is unaffected.
    if (declaration === undefined || declaration.row === 'never') return
    const entry: WaitingCall = {
      call,
      timer: null,
      timerFired: false,
      deadlineMs: rendererDeadlineMs(call.method),
    }
    waiting.set(call.id, entry)
    entry.timer = setTimeout(() => {
      entry.timer = null
      entry.timerFired = true
      setRows((previous) => [
        ...previous,
        {
          id: callRowID(call.id),
          kind: 'call',
          title: waitingSentence(call.method, declaration.waiting),
          destination: '',
          machine: '',
          // It has not completed and it has no bytes: the row is the wait
          // itself, so the phase is live and the progress values are absent.
          phase: 'running',
          done: null,
          total: null,
          speedBytesPerSecond: null,
          error: null,
          startedAt: call.startedAt,
          endedAt: null,
          cancel: () => dispatcher.cancel(call.id),
        },
      ])
    }, thresholdMs)
  }

  // A ROW IS THE WAIT, AND IT GOES WHEN THE WAIT DOES (ADR-8eej3e66 §8): "a
  // wait row goes the moment it settles and leaves no finished entry, because
  // only transfers have an outcome worth keeping". A cancelled mutation's row
  // used to stay behind to say which of the three answers it was, re-asking
  // the record every two seconds while the answer was still open — a finished
  // entry in a list the record says is not a log, and the row a person's
  // Cancel could not remove. The answer belongs to the call that was made,
  // whose own rejection carries it (AbortError for a withdrawal, the replay
  // refusal for a mutation the record already ran), not to a row.
  const onSettled = (id: number): void => {
    remove(id)
  }

  dispatcher.onCallPending = onPending
  dispatcher.onCallSettled = onSettled
  return {
    source: rows,
    dispose: () => {
      if (dispatcher.onCallPending === onPending) dispatcher.onCallPending = undefined
      if (dispatcher.onCallSettled === onSettled) dispatcher.onCallSettled = undefined
      for (const entry of waiting.values()) {
        if (entry.timer !== null) clearTimeout(entry.timer)
      }
      waiting.clear()
      setRows([])
    },
  }
}

/** The row identity of an INVOCATION, as this source and
 *  `install-operations.ts` both spell it. One call can have two rows worth of
 *  news — it is waiting, and something the backend is doing inside it is worth
 *  watching — and the model shows one row per id, so both report under the
 *  call's own key and the more specific one wins. Exported rather than copied:
 *  a second spelling of the key is how one operation becomes two rows. */
export function callRowID(id: number | string): string {
  return `call:${id}`
}
