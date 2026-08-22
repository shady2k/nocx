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
// `running` and `unsettled` both can — the second especially, because
// `unsettled` is the renderer having lost sight of a transfer the backend
// may still be writing, and files.uploadCancel is exactly what reaches it.

import { isTerminalPhase } from '../ui/operation'
import type { Operation, OperationSource } from '../operations/operations'
import type { UploadStore } from './upload-store'

export function uploadOperations(store: UploadStore): OperationSource {
  return (): Operation[] =>
    store.transfers().map((t): Operation => ({
      id: t.transferId,
      kind: 'upload',
      // The name actually written, once there is one: keepBoth may have
      // changed it, and the row must say what is on the far side.
      title: t.finalName !== '' ? t.finalName : t.name,
      destination: t.destDir,
      phase: t.phase,
      done: t.bytes,
      total: t.size,
      speedBytesPerSecond: t.speedBytesPerSecond,
      error: t.error,
      endedAt: t.endedAt,
      cancel: isTerminalPhase(t.phase) ? null : () => store.cancel(t.transferId),
    }))
}
