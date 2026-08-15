// Connection notice — the transport's condition, stated where a person is
// already looking (nocx-gbhwh).
//
// The bead's bug was silence: `Dispatcher.onDisconnect` existed and nothing
// in the product subscribed to it, so a dropped socket left every running
// block spinning while the product said nothing. This module is that
// subscriber — ONE subscriber and the sentences it renders. The dispatcher
// owns the socket and its lifecycle; the WSClient owns sessions and their
// reattach; nothing here re-derives either.
//
// A dropped connection is a persistent condition, so it is not a toast (a
// toast fades whether or not the condition has come back): it is a bar in
// the tab bar, like the update notice, that stays until the socket is back
// and the sessions' fate is known.
import { createSignal, untrack, type Accessor, type Component } from 'solid-js'
import { render } from 'solid-js/web'
import { Dispatcher } from './dispatcher'

/**
 * The notice's state. The sentence is derived from the kind; `restored`
 * carries the reattach aggregate so the sentence can distinguish "session
 * resumed" from "session lost on reconnect" — never guessing which.
 * Module-local on purpose: the exported controller interface references it,
 * and consumers drive it structurally ({ kind: 'gone' }) — knip's dead-type
 * ratchet counts an exported type only when another module imports its NAME.
 */
type ConnectionNoticeState =
  | { kind: 'hidden' }
  | { kind: 'reconnecting' }
  | { kind: 'gone' }
  | { kind: 'resuming' }
  | { kind: 'restored'; resumed: number; lost: number }

/** The session-reattach aggregate the notice consumes (owned by WSClient). */
export interface SessionReattachSeam {
  onReconnectResult(cb: (r: { resumed: number; lost: number }) => void): () => void
}

export interface ConnectionNoticeController {
  /** The current state, read by the view. */
  readonly state: Accessor<ConnectionNoticeState>
  /** Drive the notice to a state. The event wiring calls this; tests drive
   *  the branches no live event reaches (e.g. the deliberate-teardown
   *  "gone" sentence, which the dispatcher's close() never delivers). */
  setState(s: ConnectionNoticeState): void
}

/**
 * How long the good-news "restored / resumed" sentence stays before
 * dismissing itself — an outcome, not a condition, so it follows the kit's
 * 4 s success-toast duration. The loss sentence is sticky: work died, and a
 * fact that important must not fade while the user is not looking.
 */
const RESTORED_VISIBLE_MS = 4000

/** The sentence for a state — the product's words, one owner. */
function connectionNoticeSentence(s: ConnectionNoticeState): string | null {
  switch (s.kind) {
    case 'hidden':
      return null
    case 'reconnecting':
      return 'Connection lost — reconnecting…'
    case 'gone':
      return 'Connection lost'
    case 'resuming':
      return 'Connection restored — resuming session…'
    case 'restored': {
      if (s.lost > 0) {
        return s.lost === 1
          ? 'Connection restored — session lost on reconnect'
          : `Connection restored — ${s.lost} sessions lost on reconnect`
      }
      if (s.resumed > 0) {
        return s.resumed === 1
          ? 'Connection restored — session resumed'
          : `Connection restored — ${s.resumed} sessions resumed`
      }
      return 'Connection restored'
    }
  }
}

const ConnectionNoticeView: Component<{ state: Accessor<ConnectionNoticeState> }> = (props) => {
  const hidden = () => props.state().kind === 'hidden'
  const tone = (): string => {
    const s = props.state()
    switch (s.kind) {
      case 'reconnecting':
        return 'warning'
      case 'gone':
        return 'danger'
      case 'resuming':
        return 'neutral'
      case 'restored':
        return s.lost > 0 ? 'danger' : 'ok'
      default:
        return 'neutral'
    }
  }
  return (
    <div
      class="connection-notice"
      hidden={hidden()}
      data-kind={props.state().kind}
      data-tone={tone()}
    >
      {connectionNoticeSentence(props.state())}
    </div>
  )
}

/**
 * The disconnect event's decision, named so it can be asserted for both
 * inputs against production code (nocx-gbhwh): a pending reconnect means
 * the product is coming back ("reconnecting"); no pending reconnect means
 * it is not ("gone").
 *
 * The no-pending input has NO live path today: an unexpected drop always
 * schedules a retry before the event fires, and a deliberate close() clears
 * the subscribers (and production never calls close() at all). The sentence
 * is the deliberate-teardown / no-retry branch; whether the dispatcher ever
 * gains a real no-retry condition (e.g. giving up after repeated failures)
 * is a lifecycle-contract decision for the owner, not this subscriber's.
 */
export function connectionNoticeStateForDisconnect(
  reconnectPending: boolean,
): ConnectionNoticeState {
  return reconnectPending ? { kind: 'reconnecting' } : { kind: 'gone' }
}

/**
 * Mount the connection notice into the tab bar and subscribe it to the
 * transport's lifecycle. Returns the controller; the wiring in main.tsx
 * ignores the return value, tests use it to reach states no live event
 * produces.
 */
export function mountConnectionNotice(
  bar: HTMLElement,
  dispatcher: Dispatcher,
  sessions: SessionReattachSeam,
): ConnectionNoticeController {
  const host = document.createElement('div')
  bar.append(host)

  const [state, setState] = createSignal<ConnectionNoticeState>({ kind: 'hidden' })
  let restoreTimer: ReturnType<typeof setTimeout> | null = null

  const apply = (s: ConnectionNoticeState): void => {
    if (restoreTimer !== null) {
      clearTimeout(restoreTimer)
      restoreTimer = null
    }
    if (s.kind === 'restored' && s.lost === 0) {
      restoreTimer = setTimeout(() => setState({ kind: 'hidden' }), RESTORED_VISIBLE_MS)
    }
    setState(s)
  }

  // The disconnect event fires AFTER the reconnect policy has been decided
  // (dispatcher.ts), so reconnectPending here is the state that will hold.
  dispatcher.onDisconnect(() => {
    apply(connectionNoticeStateForDisconnect(dispatcher.reconnectPending))
  })

  dispatcher.onConnect(() => {
    // The socket is back; the sessions' fate is not yet known (the reattach
    // pass is in flight), so say the connection is restored and the session
    // is resuming — never claim "resumed" before the pass settles. The read
    // is a one-shot guard at event time, not a reactive dependency.
    if (untrack(state).kind === 'reconnecting' || untrack(state).kind === 'gone') {
      apply({ kind: 'resuming' })
    }
  })

  sessions.onReconnectResult((r) => {
    // Only the CURRENT pass's outcome may land: a drop resets the notice
    // out of 'resuming' in the same close event that rejects that pass's
    // attach promises (which settle in later microtasks), so an aggregate
    // from a superseded pass can never find the notice in 'resuming'.
    if (untrack(state).kind !== 'resuming') return
    apply({ kind: 'restored', resumed: r.resumed, lost: r.lost })
  })

  render(() => <ConnectionNoticeView state={state} />, host)

  return { state, setState: apply }
}
