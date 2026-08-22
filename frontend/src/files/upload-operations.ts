// The upload store, read as operations.
//
// A projection and nothing else: no state of its own, no second list, no
// decision the store has not already made. It exists because the operations
// indicator must not know what an upload is — download joins the same list
// through a function of exactly this shape, and a surface that reached into
// UploadStore directly would have to grow a second reach for it.
//
// The one judgement here is `cancel`, and it is the store's rule restated
// where the item can carry it: a terminal transfer cannot be stopped, and
// `queued`, `running` and `unsettled` all can — `unsettled` especially,
// because it is the renderer having lost sight of a transfer the backend
// may still be writing, and files.uploadCancel is exactly what reaches it.
//
// WHAT stopping means is deliberately not decided here. A queued file has
// no transfer to cancel and a running one does, and a projection that knew
// the difference would be a second owner of it — so the item carries one
// call, `store.cancel(row)`, and the store answers it (nocx-hbdw4.6).

import { isTerminalPhase } from '../ui/operation'
import type { Operation, OperationSource } from '../operations/operations'
import type { UploadStore } from './upload-store'

export function uploadOperations(store: UploadStore): OperationSource {
  return (): Operation[] =>
    store.transfers().map((t): Operation => ({
      // THE ROW'S OWN ID, never the wire's: a queued file has no transferId,
      // and the surface keys its rows on this, so an operation must not
      // change identity when the backend finally names it.
      id: t.id,
      kind: 'upload',
      // The name actually written, once there is one: keepBoth may have
      // changed it, and the row must say what is on the far side.
      title: t.finalName !== '' ? t.finalName : t.name,
      destination: t.destDir,
      machine: t.machine,
      phase: t.phase,
      done: t.bytes,
      total: t.size,
      speedBytesPerSecond: t.speedBytesPerSecond,
      error: t.error,
      startedAt: t.startedAt,
      endedAt: t.endedAt,
      cancel: isTerminalPhase(t.phase) ? null : () => store.cancel(t.id),
    }))
}
