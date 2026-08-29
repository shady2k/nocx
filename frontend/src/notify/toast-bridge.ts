// notify.toast — the toast sink's renderer half (nocx-c6ef, plan D2).
//
// A toast is a SINK on the backend, not a decision made here: the router
// resolved the route before any sink ran (ADR-0047 §2.3), and what arrives is
// an instruction to present, never an invitation to decide whether to. So
// this module has exactly one job — hand the event to the kit's toast — and
// no policy of its own.
//
// The kit's toast, deliberately. `showToast` already owns levels, dismissal,
// stacking and the overlay it draws in (frontend/src/ui/toast.tsx), and a
// second toast built for notifications would be a second vocabulary for one
// concept — the defect two epics spent themselves unwinding.
//
// Title and body are untrusted presentation data written by whatever the user
// ran (ADR-0047 §2.3). They are rendered as TEXT by the kit and are never
// spliced into markup or any other syntax here.

import type { Dispatcher } from '../dispatcher'
import type { NotifyToast } from '../generated/notify.toast'
import { showToast } from '../ui/toast'

/** The subset of Dispatcher this needs. Narrowed rather than re-declared. */
export type ToastDispatcherLike = Pick<Dispatcher, 'subscribe'>

/** The levels the wire may carry, which are the kit's own four — the contract
 *  pins the enum so the renderer never has to invent a fifth. */
const LEVELS = new Set<NotifyToast['level']>(['info', 'success', 'warning', 'danger'])

/** One line for a two-field notification. OSC 9 carries a body alone and no
 *  title; a title with no body is equally legitimate. The separator appears
 *  only when there are two things to separate — "deploy failed — exit status
 *  1" — because a toast is one line and a dangling dash is not information. */
export function toastMessage(title: string, body: string): string {
  if (title === '') return body
  if (body === '') return title
  return `${title} — ${body}`
}

/**
 * Subscribe to toast pushes and present each one with the kit's toast.
 * Returns the unsubscribe.
 */
export function subscribeNotifyToast(dispatcher: ToastDispatcherLike): () => void {
  return dispatcher.subscribe('notify.toast', (params: unknown) => {
    const push = params as NotifyToast | null
    if (typeof push?.title !== 'string' || typeof push.body !== 'string') return
    const message = toastMessage(push.title, push.body)
    // Nothing to present is not a toast. A blank one would occupy the
    // overlay saying nothing, which is worse than the drop.
    if (message === '') return
    showToast({
      message,
      // The level is nocx's stamp, never the program's — a program cannot
      // forge danger. An unknown value means the wire and this build
      // disagree about the enum, and info is the reading that presents the
      // message without claiming a severity nobody declared.
      level: LEVELS.has(push.level) ? push.level : 'info',
    })
  })
}
