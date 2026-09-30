// DownloadFlow owns the client-side lifecycle from destination preparation
// through transfer completion. It asks for a destination before minting so
// cancelling a native dialog creates no backend transfer. Once minted, the
// one-shot ticket is never retried: the prepared receiver reports a native
// outcome for completion, while browser downloads remain handed off to the
// browser and terminal accounting remains owned by files.downloadDone.

import type { ToastLevel } from '../ui/toast'
import type { FilesDownloadResult } from '../generated/files.download'
import type { DownloadServices } from './download-client'
import type { DownloadSaveOutcome, DownloadSaver, PreparedDownloadSave } from './download-save'
import type { DownloadStore } from './download-store'

/** Which file and binding the Files panel selected. */
interface DownloadTarget {
  bindingId: string
  path: string
  name: string
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
    let prepared: PreparedDownloadSave | null
    try {
      prepared = await saver.prepare(target.name)
    } catch (e) {
      report(`Could not prepare download: ${e instanceof Error ? e.message : String(e)}`, 'danger')
      return
    }
    if (prepared === null) return

    let result: FilesDownloadResult
    try {
      result = await services.download({
        bindingId: target.bindingId,
        path: target.path,
        ...(prepared.destination === 'native' ? { destination: 'native' as const } : {}),
      })
    } catch (e) {
      prepared.dispose()
      report(
        `Could not download ${target.path}: ${e instanceof Error ? e.message : String(e)}`,
        'danger',
      )
      return
    }

    const localCancel = prepared.destination === 'native' ? () => prepared.cancel() : undefined
    store.begin({
      transferId: result.transferId,
      name: result.name,
      sourcePath: target.path,
      machine: target.machine,
      size: result.size,
      localCancel,
    })

    const url = services.resolveUrl(result.url)
    if (url === null && prepared.destination === 'browser') {
      const why = `${result.name}: there is no connection to the backend to fetch the bytes over`
      store.failLocally(result.transferId, why)
      store.cancel(result.transferId)
      prepared.dispose()
      report(why, 'danger')
      return
    }

    let outcome: DownloadSaveOutcome
    try {
      outcome = await prepared.save(result, url)
    } catch {
      outcome = 'destination-failed'
    }
    if (prepared.destination !== 'native') {
      if (outcome !== 'handed-off') {
        store.failLocally(result.transferId, `${result.name}: browser could not start the download`)
        store.cancel(result.transferId)
      }
      prepared.dispose()
      return
    }

    const completion = outcome === 'handed-off' ? 'destination-failed' : outcome
    try {
      await services.complete(result.transferId, completion)
    } catch {
      store.unsettle(result.transferId, 'The native download outcome was not confirmed')
    } finally {
      prepared.dispose()
    }
  }

  return { fetch: fetchOne }
}
