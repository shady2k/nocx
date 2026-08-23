// The download surface, composed once.
//
// ONE store for the whole app, for the reason `upload-surface.ts` gives:
// a transfer has one state, and two stores subscribed to the same
// notifications would each mint a row for every transfer the other started.
// Keyed on the dispatcher, so the app's one dispatcher means one surface
// and a test that builds its own gets its own — nothing is global.
//
// It also owns THE SAME REPORT AN UPLOAD'S FAILURE GETS, through the same
// seam and deliberately not through a parallel one: `subscribeDone` ->
// `showToast` on a `failed` outcome. A person must learn a transfer failed
// wherever they are, and the operations indicator is a badge they may not
// be looking at.
//
// Two differences from the upload surface, both the direction rather than
// an omission. There is no stranded-paths warning, because a download
// creates nothing on the far host and can leave nothing behind. And a
// `cancelled` outcome is silent, exactly as it is for an upload: the person
// asked for it, and telling them what they just did is noise.

import type { Dispatcher } from '../dispatcher'
import { showToast } from '../ui/toast'
import { createDownloadServices, type DownloadServices } from './download-client'
import { createDownloadFlow, type DownloadFlow } from './download-flow'
import { createBrowserDownloadSaver } from './download-save'
import { createDownloadStore, type DownloadStore } from './download-store'

export interface DownloadSurface {
  services: DownloadServices
  store: DownloadStore
  flow: DownloadFlow
}

/** Not exported: `downloadSurfaceFor` is the only way in, because a second
 *  surface for the same dispatcher is the two-stores defect this module
 *  exists to prevent. */
function createDownloadSurface(dispatcher: Dispatcher): DownloadSurface {
  const services = createDownloadServices(dispatcher)
  const store = createDownloadStore({ services })
  const flow = createDownloadFlow({
    services,
    store,
    saver: createBrowserDownloadSaver(),
    report: (message, level) => showToast({ message, level }),
  })
  services.subscribeDone((p) => {
    if (p.outcome === 'failed') {
      // The name is always on the frame, including on a failure, so the
      // person can tell which of two downloads this was.
      const why = p.error !== undefined && p.error !== '' ? `: ${p.error}` : '.'
      showToast({ message: `Download failed — ${p.name}${why}`, level: 'danger' })
    }
  })
  return { services, store, flow }
}

/** One surface per dispatcher, created on first ask. Memoised rather than
 *  global: the app has exactly one dispatcher and therefore exactly one
 *  surface, while a test that makes its own gets its own. */
const surfaces = new WeakMap<Dispatcher, DownloadSurface>()

export function downloadSurfaceFor(dispatcher: Dispatcher): DownloadSurface {
  const existing = surfaces.get(dispatcher)
  if (existing !== undefined) return existing
  const created = createDownloadSurface(dispatcher)
  surfaces.set(dispatcher, created)
  return created
}
