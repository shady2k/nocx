// The download surface, composed once.
//
// ONE store for the whole app, for the reason `upload-surface.ts` gives:
// a transfer has one state, and two stores subscribed to the same
// notifications would each mint a row for every transfer the other started.
// Keyed on the dispatcher, so the app's one dispatcher means one surface
// and a test that builds its own gets its own — nothing is global.
//
// IT NO LONGER TOASTS A TRANSFER'S OUTCOME, for upload-surface.ts's reason
// and in the same commit (nocx-zlxmm). It used to subscribe to
// files.downloadDone and call showToast itself on a `failed` outcome —
// straight into the overlay, past the notification pipeline, so nothing
// remained afterwards for the notification centre to show.
//
// The backend raises that outcome now, at `settleDownload`, as an attested
// `transfer.finished` carrying the same name and the same reason, and it
// reaches this same `showToast` through `notify.toast`
// (frontend/src/notify/toast-bridge.ts). Keeping the subscription would give
// a person two toasts for one download. The brief for this work named only
// the upload surface; leaving this one would have doubled the download
// instead, which is the same defect wearing the other direction's clothes.
//
// `report` stays, and is not the same fact: it fires where files.download
// was refused or where there is no connection to fetch the bytes over —
// states in which no backend transfer exists to settle at all.

import type { Dispatcher } from '../dispatcher'
import {
  HostDiscardDownload,
  HostPrepareDownload,
  HostSaveDownload,
} from '../../bindings/github.com/shady2k/nocx/wailsapp'
import { hasWailsWebview } from '../wails-runtime'
import { showToast } from '../ui/toast'
import { createDownloadServices, type DownloadServices } from './download-client'
import { createDownloadFlow, type DownloadFlow } from './download-flow'
import {
  createBrowserDownloadSaver,
  createNativeDownloadSaver,
  type DownloadSaver,
} from './download-save'
import { createDownloadStore, type DownloadStore } from './download-store'

export interface DownloadSurface {
  services: DownloadServices
  store: DownloadStore
  flow: DownloadFlow
}

/** Not exported: `downloadSurfaceFor` is the only way in, because a second
 *  surface for the same dispatcher is the two-stores defect this module
 *  exists to prevent. */
export interface DownloadSurfaceOptions {
  native?: () => boolean
  browserSaver?: DownloadSaver
  nativeSaver?: DownloadSaver
}

function createDownloadSurface(
  dispatcher: Dispatcher,
  options: DownloadSurfaceOptions,
): DownloadSurface {
  const services = createDownloadServices(dispatcher)
  const store = createDownloadStore({ services })
  const native = options.native ?? hasWailsWebview
  const saver = native()
    ? (options.nativeSaver ??
      createNativeDownloadSaver({
        prepare: HostPrepareDownload,
        save: HostSaveDownload,
        discard: HostDiscardDownload,
      }))
    : (options.browserSaver ?? createBrowserDownloadSaver())
  const flow = createDownloadFlow({
    services,
    store,
    saver,
    report: (message, level) => showToast({ message, level }),
  })
  return { services, store, flow }
}

/** One surface per dispatcher, created on first ask. Memoised rather than
 *  global: the app has exactly one dispatcher and therefore exactly one
 *  surface, while a test that makes its own gets its own. */
const surfaces = new WeakMap<Dispatcher, DownloadSurface>()

export function downloadSurfaceFor(
  dispatcher: Dispatcher,
  options: DownloadSurfaceOptions = {},
): DownloadSurface {
  const existing = surfaces.get(dispatcher)
  if (existing !== undefined) return existing
  const created = createDownloadSurface(dispatcher, options)
  surfaces.set(dispatcher, created)
  return created
}
