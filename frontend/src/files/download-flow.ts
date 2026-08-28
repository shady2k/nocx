// DownloadFlow — take one file off the machine the active tab is on.
//
// It is shorter than the upload flow and every missing part is the
// direction rather than an economy. There is no collision question because
// nothing on the far host is replaced. There is no source routing because
// there is one source: a path the caller already enumerated. There is no
// body to send because the bytes come the other way, and they do not come
// through the renderer at all (`download-save.ts`).
//
// The whole gesture is: mint a transfer, record it from the RESULT, and hand
// its opaque id plus the browser URL to the selected platform saver.
//
// - `files.download` may be refused before a transfer exists; then no row is
//   minted.
// - A browser may have no socket origin with which to redeem the URL; its
//   unclaimed ticket will expire, so the renderer marks that local half failed.
// - A native start may reject after the backend claimed the transfer; the error
//   is reported immediately, while `files.downloadDone` remains the only writer
//   of its terminal outcome.
//
// Everything after the save is the backend's account: files.downloadProgress
// moves the row and files.downloadDone ends it.

import type { ToastLevel } from '../ui/toast'
import type { DownloadServices } from './download-client'
import { DownloadNotClaimedError, type DownloadSaver } from './download-save'
import type { DownloadStore } from './download-store'

/** Which file, on which binding. The name is not here: the backend measures
 *  and names the file on the handle it opens, and a name carried from the
 *  tree would be the renderer's second opinion about it.
 *
 *  Not exported: a caller passes the object literal, never names the type —
 *  the rule `upload-flow.ts`'s collision ask follows. */
interface DownloadTarget {
  bindingId: string
  path: string
  /** WHICH MACHINE `path` is on, as a person names it. It rides the target
   *  rather than being derived here for the reason `UploadDestination`
   *  carries it: `machine-name.ts` is the one owner of the string and its
   *  answer is already on the origin the panel follows, so a flow that
   *  built its own would be a second spelling of one machine — and the two
   *  agree everywhere anybody looks until the day one of them has no user. */
  machine: string
}

/** Tell the person something went wrong. A refusal is an action outcome and
 *  must be visible in the product, never only in a log. Not exported for
 *  the reason above: a caller passes a function. */
type DownloadReport = (message: string, level: ToastLevel) => void

export interface DownloadFlow {
  /** Fetch one file. Never rejects: every failure is reported. */
  fetch(target: DownloadTarget): Promise<void>
}

export interface DownloadFlowDeps {
  services: DownloadServices
  store: DownloadStore
  saver: DownloadSaver
  report: DownloadReport
}

export function createDownloadFlow(deps: DownloadFlowDeps): DownloadFlow {
  const { services, store, saver, report } = deps

  async function fetchOne(target: DownloadTarget): Promise<void> {
    let result
    try {
      result = await services.download({ bindingId: target.bindingId, path: target.path })
    } catch (e) {
      // No transfer was created, so there is nothing to put in the
      // operations list — a row here would never receive a done frame and
      // would sit unfinished for the life of the session.
      report(
        `Could not download ${target.path}: ${e instanceof Error ? e.message : String(e)}`,
        'danger',
      )
      return
    }
    // The RESULT is what says the transfer exists — never a progress frame.
    store.begin({
      transferId: result.transferId,
      name: result.name,
      sourcePath: target.path,
      machine: target.machine,
      size: result.size,
    })
    const url = services.resolveUrl(result.url)
    // Exactly once. Browser spends the one-shot ticket with the anchor;
    // native spends the same ticket inside files.downloadSave before its
    // dialog opens. A rejected native start is visible here while the
    // backend's files.downloadDone remains the authoritative terminal row.
    try {
      await saver.save({ transferId: result.transferId, name: result.name, url })
    } catch (e) {
      const detail = e instanceof Error ? e.message : String(e)
      if (e instanceof DownloadNotClaimedError) {
        store.failLocally(result.transferId, detail)
      }
      report(
        e instanceof DownloadNotClaimedError ? detail : `Could not save ${result.name}: ${detail}`,
        'danger',
      )
    }
  }

  return { fetch: fetchOne }
}
