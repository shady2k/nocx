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
// The row is keyed `call:<requestId>`, the same key `call-operations.ts` uses
// for the call that is waiting, and the operations model shows one row per id
// (the more specific source wins). So a first open is ONE row: the renderer's
// own "Opening your terminal" steps aside for the install's account of what it
// is doing, and comes back if the open is still waiting after the install
// finished. Cancel is the same withdrawal either way — `rpc.cancel` for the
// call — because the install has no separate identity on the wire and should
// not grow one.
//
// ## The row goes on settle and leaves nothing behind
//
// `operation.rowDone` removes it, and so does a lost connection: the work is
// owned by the request, which dies with the socket, so a row that outlived the
// socket would be a row about nothing. Neither is a finished entry — only
// transfers have an outcome worth keeping.

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

/** A request id this renderer can withdraw: only the ids it mints are
 *  numbers (dispatcher.ts's own counter). A row for somebody else's string
 *  id still draws; it just carries no Cancel, because the row's one
 *  affordance is a judgement about THIS operation and this renderer cannot
 *  make it. */
function cancellableID(id: number | string): number | null {
  return typeof id === 'number' ? id : null
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
  // When the row first appeared, on this source's clock — the opening end of
  // the duration a row reports if it is ever finished. Kept out of the row so
  // a frame that updates the title does not restart it.
  const startedAt = new Map<string, number>()

  const show = (params: OperationRow): void => {
    const id = callRowID(params.requestId)
    const started = startedAt.get(id) ?? Date.now()
    startedAt.set(id, started)
    const known = params.total > 0
    const cancelID = cancellableID(params.requestId)
    const row: Operation = {
      id,
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
      cancel: cancelID === null ? null : () => dispatcher.cancel(cancelID),
    }
    setRows((previous) => [...previous.filter((r) => r.id !== id), row])
  }

  const gone = (id: number | string): void => {
    const rowID = callRowID(id)
    startedAt.delete(rowID)
    setRows((previous) => previous.filter((r) => r.id !== rowID))
  }

  const unsubscribe = [
    dispatcher.subscribe('operation.row', (params: unknown) => {
      if (isRowParams(params)) show(params)
    }),
    dispatcher.subscribe('operation.rowDone', (params: unknown) => {
      if (!isRowDoneParams(params)) return
      gone(params.requestId)
    }),
    // A DROPPED CONNECTION IS THE ROW'S END TOO. The work belongs to the
    // request, the request dies with the socket, and nothing replays a row on
    // reconnect — so a row that survived the socket would be a row about
    // nothing.
    dispatcher.onDisconnect(() => {
      startedAt.clear()
      setRows([])
    }),
  ]

  return {
    source: rows,
    dispose: () => {
      for (const stop of unsubscribe) stop()
      startedAt.clear()
      setRows([])
    },
  }
}
