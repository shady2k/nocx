// DownloadFlow — take one file off the machine the active tab is on.
//
// It is shorter than the upload flow and every missing part is the
// direction rather than an economy. There is no collision question because
// nothing on the far host is replaced. There is no source routing because
// there is one source: a path the caller already enumerated. There is no
// body to send because the bytes come the other way, and they do not come
// through the renderer at all (`download-save.ts`).
//
// So the whole gesture is: mint a transfer, record it from the RESULT, and
// hand the URL to the platform. Two things can go wrong here and both are
// the renderer's own half:
//
// - `files.download` was refused. No transfer exists, so there is no row to
//   fail — the refusal is reported and nothing else happens. (A row minted
//   for a transfer the backend never created is a row nothing can ever end.)
// - The URL cannot be resolved, which means there is no connection to
//   resolve it against. The transfer DOES exist, the ticket will expire
//   unredeemed, and the row says so rather than sitting at 0% forever.
//
// Everything after the save is the backend's account: files.downloadProgress
// moves the row and files.downloadDone ends it.

import type { ToastLevel } from '../ui/toast'
import type { DownloadServices } from './download-client'
import type { DownloadSaver } from './download-save'
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
    if (url === null) {
      const why = `${result.name}: there is no connection to the backend to fetch the bytes over`
      store.failLocally(result.transferId, why)
      report(why, 'danger')
      return
    }
    // Exactly once. The ticket is one-shot: a retry would be a 410, which
    // would report a failure for a file that may already be on the disk.
    saver.save(url)
  }

  return { fetch: fetchOne }
}
