// The download store, read as operations.
//
// The second implementation of `OperationSource`, and the thing worth
// noticing is how little it took: the operations model, the indicator and
// the row learn nothing about downloads, because `upload-operations.ts` was
// written as a projection rather than as the list itself. A surface that
// had reached into `UploadStore` directly would be growing a second reach
// here instead.
//
// A projection and nothing else: no state of its own, no second list, no
// decision the store has not already made. The one judgement is `cancel`,
// and it is the store's rule restated where the item can carry it — a
// terminal transfer cannot be stopped, and `running` and `unsettled` both
// can, the second especially, because `unsettled` is the renderer having
// lost sight of a transfer the backend may still be sending.

import { isTerminalPhase } from '../ui/operation'
import type { Operation, OperationSource } from '../operations/operations'
import type { DownloadStore } from './download-store'

export function downloadOperations(store: DownloadStore): OperationSource {
  return (): Operation[] =>
    store.transfers().map((t): Operation => ({
      id: t.transferId,
      kind: 'download',
      title: t.name,
      // WHERE IT CAME FROM, not where it is going, and the row's label for
      // this field says "destination" — which is the honest read of it
      // here, because a download's destination is the one thing nobody
      // knows. The browser chose it, by the platform's own mechanism, and
      // never told the page. Showing the source path is what lets a person
      // tell two files of the same name apart; showing a guessed
      // "~/Downloads" would be the renderer inventing an answer.
      destination: t.sourcePath,
      // WHICH machine that path is on. It matters more here than for an
      // upload, not less: the list is global, and "/etc/nginx.conf" with no
      // machine beside it is a path that exists on every host a person has
      // open. The store recorded it at the start, because by the time this
      // row is drawn there is no tab left to ask.
      machine: t.machine,
      phase: t.phase,
      done: t.bytes,
      total: t.size,
      speedBytesPerSecond: t.speedBytesPerSecond,
      error: t.error,
      startedAt: t.startedAt,
      endedAt: t.endedAt,
      cancel: isTerminalPhase(t.phase) ? null : () => store.cancel(t.transferId),
    }))
}
