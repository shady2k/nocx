// THE SWITCH FENCE WINDOW, read from the wire trace the alternate-screen
// spec records.
//
// It lives outside the spec for one reason: the reading can then be decided by
// a trace that is already in hand instead of by a race in a PTY. The spec's
// own analysis was wrong once (nocx-1ebrs.66.1.25), and a wrong analysis is
// invisible for as long as the race happens to go the other way — which is how
// a green suite and a red runner can both be honest.
//
// The vocabulary is the trace's, not the product's: `send` is a request the
// pane put on the socket, `resize-receipt` is an answer to one of them, and
// `frame` is a screen frame the runtime published. Nothing here knows what the
// client believed; it reads what crossed the wire.
export type SwitchTraceEvent = Record<string, unknown> & { at: number; kind: string }

export interface SwitchFenceWindow {
  /** The first frame the runtime published with the alternate buffer, or -1. */
  switchIndex: number
  /** The first resize the switch sent, or -1. */
  firstResize: number
  /** The id of the request `firstResize` sent. */
  switchResizeId: number | undefined
  /** The first input the pane sent after the switch, or -1. */
  firstInput: number
  /** The applied receipt that released the fence, or -1. */
  release: number
  /** The id of the request that receipt answered. */
  releaseId: number | undefined
  /** Input the pane sent between the switch's resize and its release. */
  inputsBeforeRelease: SwitchTraceEvent[]
  /** Where the switch's own request was answered inside that window, or -1. */
  answered: number
}

const isResizeSend = (event: SwitchTraceEvent): boolean =>
  event.kind === 'send' && event.method === 'resize'
const isIntentSend = (event: SwitchTraceEvent): boolean =>
  event.kind === 'send' && event.method === 'session.intent'
const isAppliedReceipt = (event: SwitchTraceEvent): boolean =>
  event.kind === 'resize-receipt' && 'result' in event && !event.error

const NOTHING: SwitchFenceWindow = {
  switchIndex: -1,
  firstResize: -1,
  switchResizeId: undefined,
  firstInput: -1,
  release: -1,
  releaseId: undefined,
  inputsBeforeRelease: [],
  answered: -1,
}

/**
 * Where the fence opened, which request it sent, and which receipt released
 * it. Throws nothing and reads nothing: a trace that never reached the
 * alternate buffer comes back with -1 in every field, which the spec asserts
 * on with its own messages.
 */
export function switchFenceWindow(trace: SwitchTraceEvent[]): SwitchFenceWindow {
  const switchIndex = trace.findIndex(
    (event) =>
      event.kind === 'frame' &&
      (event.state as { bufferKind?: string } | undefined)?.bufferKind === 'alternate',
  )
  if (switchIndex < 0) return NOTHING
  const firstResize = trace.findIndex((event, index) => index > switchIndex && isResizeSend(event))
  const firstInput = trace.findIndex((event, index) => index > switchIndex && isIntentSend(event))
  if (firstResize < 0) return { ...NOTHING, switchIndex, firstInput }
  const switchResizeId = trace[firstResize].id as number | undefined
  // THE RECEIPT THAT RELEASES THE FENCE ANSWERS A RESIZE THE SWITCH SENT
  // (nocx-1ebrs.66.1.25). The fence opens when the buffer becomes alternate,
  // so every request it makes is on the socket after that frame. A receipt
  // for a request sent BEFORE the switch — the settle timer's, typically,
  // whose answer can land in the gap between the switch's own send and its
  // answer — releases nothing, and treating it as the release reported the
  // switch's request as unanswered while the wire had answered it milliseconds
  // later. That reading failed the "after the resize" variant on a release
  // runner while the product held the input exactly as designed.
  const sentByTheSwitch = new Set<number>()
  for (let index = switchIndex + 1; index < trace.length; index += 1) {
    if (isResizeSend(trace[index]) && typeof trace[index].id === 'number')
      sentByTheSwitch.add(trace[index].id as number)
  }
  const release = trace.findIndex(
    (event, index) =>
      index > firstResize && isAppliedReceipt(event) && sentByTheSwitch.has(event.id as number),
  )
  const inputsBeforeRelease =
    release < 0 ? [] : trace.slice(firstResize + 1, release).filter(isIntentSend)
  const answered =
    release < 0
      ? -1
      : trace.findIndex(
          (event, index) =>
            index > firstResize &&
            index < release &&
            event.kind === 'resize-receipt' &&
            event.id === switchResizeId &&
            ('error' in event || !('result' in event)),
        )
  return {
    switchIndex,
    firstResize,
    switchResizeId,
    firstInput,
    release,
    releaseId: release < 0 ? undefined : (trace[release].id as number | undefined),
    inputsBeforeRelease,
    answered,
  }
}
