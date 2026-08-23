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
// IT NO LONGER TOASTS A TRANSFER'S OUTCOME, and the removal is the point
// (nocx-zlxmm). It used to subscribe to files.uploadDone and call showToast
// itself for a `failed` outcome and for any stranded path — straight into
// the overlay, past the notification pipeline entirely. The toast appeared,
// expired, and nothing remained: the notification centre had no record of a
// transfer, ever, so "what did I miss" could not answer the one question
// somebody who walked away from an upload actually has.
//
// The backend raises that outcome now, at the point it becomes known
// (`settleUpload`, internal/transport/ws_transfer_notify.go), as an attested
// `transfer.finished` whose default channel is the toast. It arrives through
// `notify.toast` and lands in this same `showToast`
// (frontend/src/notify/toast-bridge.ts), so keeping the subscription here
// would be a SECOND mechanism for one fact and the person would get two
// toasts for one transfer — which is worse than the none they had before.
// It carries what both removed toasts carried: the failure's reason, and the
// paths an upload left behind.
//
// What stays is `report`, and it is not the same fact. It fires where NO
// backend transfer exists to settle — files.upload was refused, the POST
// never landed — or where the renderer's own half ended without an answer,
// which is a different sentence at a different moment from the outcome the
// backend will still raise when it settles.

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
