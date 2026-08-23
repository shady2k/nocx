// The upload surface, composed once.
//
// ONE store for the whole app, because a transfer has one state and two
// stores subscribed to the same notifications would each mint a row for
// every transfer the other started — two surfaces owning one input, which
// is the defect whichever of them wins. The two gestures live in different
// trees (the Files panel is Solid, the terminal pane is not), so the shared
// instance is keyed on the thing they DO share: the dispatcher they both
// already hold. One dispatcher in the app means one surface; a test that
// builds its own dispatcher gets its own, and nothing is global.
//
// It also owns the one report a person must get wherever they are: a
// transfer that FAILED, and any path a transfer left behind. Those are told
// as toasts because they can arrive while the Files panel is not open —
// while a running transfer's progress belongs in the panel, which is where
// somebody watching a transfer is looking. A success is deliberately not
// toasted: the destination directory is invalidated and the existing
// files.changed path re-lists it, so the file appearing IS the report, and
// a twenty-file drop would otherwise produce twenty notifications.

import type { Dispatcher } from '../dispatcher'
import { showToast } from '../ui/toast'
import { askCollision } from './ask-collision'
import { createUploadServices, type UploadServices } from './upload-client'
import { createUploadFlow, type UploadFlow } from './upload-flow'
import { createUploadStore, type UploadStore } from './upload-store'

export interface UploadSurface {
  services: UploadServices
  store: UploadStore
  flow: UploadFlow
}

/** Not exported: `uploadSurfaceFor` is the only way in, because a second
 *  surface for the same dispatcher is the two-stores defect this module
 *  exists to prevent. */
function createUploadSurface(dispatcher: Dispatcher): UploadSurface {
  const services = createUploadServices(dispatcher)
  const store = createUploadStore({ services })
  const flow = createUploadFlow({
    services,
    store,
    ask: askCollision,
    report: (message, level) => showToast({ message, level }),
  })
  services.subscribeDone((p) => {
    if (p.outcome === 'failed') {
      showToast({
        message:
          p.error !== undefined && p.error !== '' ? `Upload failed: ${p.error}` : 'Upload failed.',
        level: 'danger',
      })
    }
    // Orthogonal to the outcome, and never folded into it: a 'written'
    // transfer whose backup unlink failed succeeded AND left a file on the
    // server. Naming it is the difference between a tidy disk and a
    // mystery file nobody can explain.
    if (p.stranded.length > 0) {
      showToast({
        message: `Left behind on the server: ${p.stranded.join(', ')}`,
        level: 'warning',
      })
    }
  })
  return { services, store, flow }
}

/** One surface per dispatcher, created on first ask. Memoised rather than
 *  global: the app has exactly one dispatcher and therefore exactly one
 *  surface, while a test that makes its own gets its own. */
const surfaces = new WeakMap<Dispatcher, UploadSurface>()

export function uploadSurfaceFor(dispatcher: Dispatcher): UploadSurface {
  const existing = surfaces.get(dispatcher)
  if (existing !== undefined) return existing
  const created = createUploadSurface(dispatcher)
  surfaces.set(dispatcher, created)
  return created
}
