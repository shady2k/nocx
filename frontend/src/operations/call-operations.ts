// Call operations — the Operations source for a person's call that is still
// waiting (ADR-8eej3e66 §8).
//
// A row exists only while it means something: a person's call past two
// seconds, with the catalogue's waiting sentence and Cancel. It goes the
// moment the call settles and leaves no finished entry — only transfers have
// an outcome worth keeping. Ordinary work that answers inside the threshold
// never appears at all, which is the owner's condition against a list that
// fills with every case.
//
// Background calls never reach this source: the dispatcher announces only
// person-origin operation calls, and background work merges on its key and
// stays in the log and diagnostics.

import { createSignal } from 'solid-js'
import type { Dispatcher, PendingCallView } from '../dispatcher'
import { callOperationDeclaration } from '../generated/operations'
import { rendererDeadlineMs } from './renderer-deadlines'
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
  /** The renderer deadline projection, present only for an armed domain.
   *  None is armed at this stage, so no call gets a timer from it. */
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

  const onSettled = (id: number): void => remove(id)

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

function callRowID(id: number): string {
  return `call:${id}`
}
