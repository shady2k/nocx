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
import type { CallSettlement, Dispatcher, PendingCallView } from '../dispatcher'
import { callOperationDeclaration } from '../generated/operations'
import { resolveMutationOutcome, type MutationAnswer } from '../mutations'
import type { OperationPhase } from '../ui/operation'
import type { Operation, OperationSource } from './operations'

/** Waiting under two seconds shows the action's own pending state. Two
 *  seconds is the owner's answer (ADR-8eej3e66 §8). */
const WAIT_ROW_THRESHOLD_MS = 2000

/** How often a row still asking re-asks. It is not a deadline: the backend's
 *  own retention ends the question, and every other answer ends it at once. */
const REASK_INTERVAL_MS = 2000

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
  /** The re-ask still pending for a mutation whose answer is not final. */
  reask: ReturnType<typeof setTimeout> | null
}

/** How one of §8's answers reads in the row. It is a mapping and not a second
 *  vocabulary: the phase names come from the operation vocabulary, and the
 *  sentence from mutations.ts — the one place the three answers are worded. */
const ANSWER_PHASE: Record<MutationAnswer, OperationPhase> = {
  'not-started': 'skipped',
  done: 'written',
  'outcome-unknown': 'unsettled',
  unknown: 'outcome-unknown',
}

export function callOperations(
  dispatcher: Dispatcher,
  thresholdMs: number = WAIT_ROW_THRESHOLD_MS,
  reaskMs: number = REASK_INTERVAL_MS,
): CallOperations {
  const [rows, setRows] = createSignal<Operation[]>([])
  const waiting = new Map<number, WaitingCall>()

  const remove = (id: number): void => {
    const entry = waiting.get(id)
    if (entry === undefined) return
    if (entry.timer !== null) clearTimeout(entry.timer)
    if (entry.reask !== null) clearTimeout(entry.reask)
    waiting.delete(id)
    setRows((previous) => previous.filter((row) => row.id !== callRowID(id)))
  }

  const onPending = (call: PendingCallView): void => {
    const declaration = callOperationDeclaration(call.method)
    // A method outside the catalogue, or one the catalogue says never shows,
    // takes no row. The call itself is unaffected.
    if (declaration === undefined || declaration.row === 'never') return
    const entry: WaitingCall = { call, timer: null, timerFired: false, reask: null }
    waiting.set(call.id, entry)
    entry.timer = setTimeout(() => {
      entry.timer = null
      entry.timerFired = true
      setRows((previous) => [
        ...previous,
        {
          id: callRowID(call.id),
          kind: 'call',
          title: declaration.waiting,
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

  /** Ask what happened, and let the row say it. The row is not re-sent and
   *  never becomes a retry: this is the only follow-up a mutation gets. */
  const ask = (id: number, entry: WaitingCall): void => {
    const mutation = entry.call.mutation
    if (mutation === undefined) return
    void resolveMutationOutcome(dispatcher, mutation).then((assessment) => {
      const current = waiting.get(id)
      if (current !== entry) return
      setRows((previous) =>
        previous.map((row) =>
          row.id === callRowID(id)
            ? {
                ...row,
                phase: ANSWER_PHASE[assessment.answer],
                error: assessment.sentence,
                // Cancelling an operation that already stopped would mean
                // nothing, and the row stops offering it.
                cancel: null,
                endedAt: assessment.checking ? null : Date.now(),
              }
            : row,
        ),
      )
      if (!assessment.checking) {
        if (entry.reask !== null) clearTimeout(entry.reask)
        entry.reask = null
        return
      }
      // Still "checking what happened": the work has not finished, and the
      // backend's retention ends the question even if nothing else does.
      entry.reask = setTimeout(() => {
        entry.reask = null
        ask(id, entry)
      }, reaskMs)
    })
  }

  const onSettled = (id: number, settlement: CallSettlement): void => {
    const entry = waiting.get(id)
    if (entry === undefined || entry.call.mutation === undefined || settlement === 'answered') {
      remove(id)
      return
    }
    if (!entry.timerFired) {
      // The call ended before it ever took a row. The surface that made it
      // owns the answer there; a list of finished rows nobody waited in is
      // the failure the threshold exists to prevent.
      remove(id)
      return
    }
    // The row stops waiting and starts answering. It keeps its title, so a
    // person can still see what the question is about.
    ask(id, entry)
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
        if (entry.reask !== null) clearTimeout(entry.reask)
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
