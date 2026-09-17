/**
 * session.signalUndelivered — a Stop the person was told was ACCEPTED that will
 * not reach its command (nocx-zas0d, review findings 2 and 4 of 1e899f6a).
 *
 * WHY THIS EXISTS AT ALL. session.signal answers `held` for a Stop that arrives
 * between the submit and the shell's start, because writing the byte there puts
 * it inside bash's line parser (the suffix-execution window, nocx-xn63t.6.11).
 * The price of that answer is that the request is over: there is nobody left to
 * tell if the delivery then fails, or finds the command already gone. So the
 * backend says it here instead, once, when the obligation ends without a write.
 *
 * TWO FACTS, ONE NOTIFICATION, and the renderer needs both: the mark this pane
 * took on the acceptance (`BlockRecord.stopRequested`, which is what turns a
 * nonzero exit into "Stopped") comes off, and the person is told. Keeping the
 * mark would mean a command that ran to its own end and failed was painted as
 * one the person stopped.
 */

import type { Dispatcher } from './dispatcher'
import type { SessionSignalUndelivered } from './generated/session.signalUndelivered'
import type { ToastLevel } from './ui/toast'

/** One undelivered-Stop fact, delivered with its session id intact. */
export type SignalUndeliveredHandler = (fact: SessionSignalUndelivered) => void

/** Subscribe to the server-initiated session.signalUndelivered notification.
 *  The wire shape is guarded at the boundary like files.changed,
 *  lifecycle.changed and session.integrationChanged (the same
 *  unsolicited-notification defect class): a payload without the four strings
 *  this fact is made of is not a fact and is not delivered. */
export function subscribeSignalUndelivered(
  dispatcher: Dispatcher,
  handler: SignalUndeliveredHandler,
): () => void {
  return dispatcher.subscribe('session.signalUndelivered', (params: unknown) => {
    const p = params as SessionSignalUndelivered
    if (
      p &&
      typeof p.sessionId === 'string' &&
      typeof p.signal === 'string' &&
      typeof p.attempt === 'string' &&
      typeof p.reason === 'string'
    ) {
      handler(p)
    }
  })
}

/** What the person is told, per reason. The vocabulary is closed by the
 *  contract and this switch is exhaustive on purpose: a reason the server
 *  learns to send and this does not must be a type error here rather than a
 *  wrong sentence there.
 *
 *  `attempt-closed` is deliberately quiet news rather than a warning: the
 *  command had ended (or never ran) and the stop was moot — the block settles
 *  by the shell's own outcome, which is the truth. Every other reason is a
 *  warning, because the command may still be running and the person's next
 *  Stop reaches it by the ordinary path. */
export function undeliveredNotice(
  fact: SessionSignalUndelivered,
): { level: ToastLevel; message: string } | null {
  switch (fact.reason) {
    case 'attempt-closed':
      return {
        level: 'info',
        message: 'The command had ended before the stop could reach it.',
      }
    case 'unsupported':
      return {
        level: 'warning',
        message: 'This command is running on a host nocx cannot signal from here.',
      }
    case 'write-refused':
    case 'write-failed':
    case 'write-unconfirmed':
    case 'lane-refused':
      return {
        level: 'warning',
        message:
          'nocx could not deliver the stop to the command. The command may still be running — press Stop again to reach it.',
      }
    default: {
      const unreachable: never = fact.reason
      return {
        level: 'warning',
        message: `nocx could not stop the command (${String(unreachable)})`,
      }
    }
  }
}
