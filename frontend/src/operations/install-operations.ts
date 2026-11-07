// Install operations — the Operations source for work the BACKEND does
// inside a call a person made (ADR-8eej3e66 §8).
//
// The case it exists for is a first open of a host where the helper is not
// installed: most of that call is a 15.3 MB upload over somebody's link, and
// until this source the only row the person could see said "Opening your
// terminal" — which is true and says nothing about what is happening. The
// catalogue already declares the install as work a person is watching
// (`row: always`), so the backend reports it and this draws it.
//
// ## Why it is a source at all
//
// A row is an ADDITION here, never a change to the model, the indicator or
// the row component: `OperationSource` is the seam
// (`operations/operations.ts`), and this is the fourth function of that shape.
// What it does NOT own is when a row may exist — that is the catalogue's
// declaration, evaluated in the backend, which only ever reports a row it was
// told to show.
//
// ## It borrows the call's identity on purpose
//
// The row is keyed `call:<the call's id>`, the same key `call-operations.ts`
// uses for the call that is waiting, and the operations model shows one row
// per id (the more specific source wins). So a first open is ONE row: the
// renderer's own "Opening your terminal" steps aside for the install's
// account of what it is doing, and comes back if the open is still waiting
// after the install finished. Cancel is the same withdrawal either way —
// `rpc.cancel` for the call — because the install has no separate identity on
// the wire and should not grow one.
//
// The backend names this work by the id the call was SENT with, and that is
// not the id the call is (`Dispatcher.callIDForRequest` says why: the wire id
// is per attempt, and the renderer's id survives a sealed retry and a queue).
// The translation is the dispatcher's, because it is the only place that
// holds both. A request id that names no live call of this renderer's draws
// nothing: there is no call here to report on, no withdrawal to offer, and no
// key to use that is not some other call's.
//
// ## The row goes when the call does, and leaves nothing behind
//
// Three things end it, and the call's own settlement is the one that must:
// Cancel goes through `rpc.cancel`, the dispatcher settles the call at once,
// and a row that waited for the backend's `operation.rowDone` frame would sit
// there after a Cancel that had already worked — the frame need never come,
// because the work it would report on has been withdrawn. `rowDone` still ends
// the row on its own, for the install that finishes while the open goes on,
// and a lost connection ends it too: the work is owned by the request, which
// dies with the socket, so a row that outlived the socket would be a row about
// nothing. None of them is a finished entry — only transfers have an outcome
// worth keeping.

import { createSignal } from 'solid-js'
import type { Dispatcher } from '../dispatcher'
import type { OperationRow } from '../generated/operation.row'
import type { OperationRowDone } from '../generated/operation.rowDone'
import type { Operation, OperationSource } from './operations'
import { callRowID } from './call-operations'

export interface InstallOperations {
  /** The Operations source, read by the model. */
  source: OperationSource
  /** Stop observing. */
  dispose: () => void
}

/** One key per request id, so `1` and `"1"` stay two requests — which they
 *  are on the wire, and which one map keyed by both would lose. */
function requestKey(id: number | string): string {
  return typeof id === 'number' ? `n:${id}` : `s:${id}`
}

function isRowParams(value: unknown): value is OperationRow {
  if (typeof value !== 'object' || value === null) return false
  const p = value as Record<string, unknown>
  const id = p.requestId
  return (
    (typeof id === 'number' || typeof id === 'string') &&
    p.kind === 'install' &&
    typeof p.title === 'string' &&
    p.title !== '' &&
    typeof p.machine === 'string' &&
    typeof p.done === 'number' &&
    typeof p.total === 'number'
  )
}

function isRowDoneParams(value: unknown): value is OperationRowDone {
  if (typeof value !== 'object' || value === null) return false
  const id = (value as Record<string, unknown>).requestId
  return typeof id === 'number' || typeof id === 'string'
}

export function installOperations(dispatcher: Dispatcher): InstallOperations {
  const [rows, setRows] = createSignal<Operation[]>([])
  /** The rows drawn, by the request id the backend names them with. The
   *  call's id is the row's identity, while the wire id is what the backend
   *  goes on saying — including after an answer has made the dispatcher
   *  forget it — so the pair has to be remembered here, not looked up when the
   *  row ends. `startedAt` is this source's clock at the first frame: the
   *  opening end of the duration a row reports if it is ever finished. Kept
   *  out of the row so a frame that updates the title does not restart it. */
  const drawn = new Map<string, { rowID: string; startedAt: number }>()

  const show = (params: OperationRow): void => {
    const callID = dispatcher.callIDForRequest(params.requestId)
    if (callID === null) return
    const key = requestKey(params.requestId)
    const rowID = callRowID(callID)
    const started = drawn.get(key)?.startedAt ?? Date.now()
    drawn.set(key, { rowID, startedAt: started })
    const known = params.total > 0
    const row: Operation = {
      id: rowID,
      kind: 'install',
      title: params.title,
      // A remote path this renderer was told nothing about. An empty field
      // draws nothing, which is the honest answer (see Operation.destination).
      destination: '',
      machine: params.machine,
      // Live for its whole duration and never terminal: the row goes on
      // settle instead of becoming a finished entry.
      phase: 'running',
      // Zero total means the stage counts nothing (the verification read),
      // and null is how the row says so: no bar, no percentage.
      done: known ? params.done : null,
      total: known ? params.total : null,
      speedBytesPerSecond: null,
      error: null,
      startedAt: started,
      endedAt: null,
      // The row IS the call: its Cancel is the call's ordinary withdrawal.
      cancel: () => dispatcher.cancel(callID),
    }
    setRows((previous) => [...previous.filter((r) => r.id !== rowID), row])
  }

  const gone = (requestID: number | string): void => {
    const key = requestKey(requestID)
    const row = drawn.get(key)
    if (row === undefined) return
    drawn.delete(key)
    setRows((previous) => previous.filter((r) => r.id !== row.rowID))
  }

  /** End every row this call owns. It is keyed by the call rather than by the
   *  wire id, because the call is what settles: the dispatcher forgets the wire
   *  id with the answer, and Cancel settles a call that may still be waiting
   *  for one. */
  const callSettled = (callID: number): void => {
    const rowID = callRowID(callID)
    let found = false
    for (const [key, entry] of drawn) {
      if (entry.rowID !== rowID) continue
      drawn.delete(key)
      found = true
    }
    if (!found) return
    setRows((previous) => previous.filter((row) => row.id !== rowID))
  }

  const unsubscribe = [
    dispatcher.subscribe('operation.row', (params: unknown) => {
      if (isRowParams(params)) show(params)
    }),
    dispatcher.subscribe('operation.rowDone', (params: unknown) => {
      if (!isRowDoneParams(params)) return
      gone(params.requestId)
    }),
    // THE ROW'S END IS THE CALL'S SETTLEMENT. Cancel is settled in the
    // dispatcher, so the answer arrives here without the backend having to
    // send anything at all.
    dispatcher.onCallSettled(callSettled),
    // A DROPPED CONNECTION IS THE ROW'S END TOO. The work belongs to the
    // request, the request dies with the socket, and nothing replays a row on
    // reconnect — so a row that survived the socket would be a row about
    // nothing.
    dispatcher.onDisconnect(() => {
      drawn.clear()
      setRows([])
    }),
  ]

  return {
    source: rows,
    dispose: () => {
      for (const stop of unsubscribe) stop()
      drawn.clear()
      setRows([])
    },
  }
}
