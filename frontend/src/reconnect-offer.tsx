// The offer a lost pane makes.
//
// A session that ended is TERMINAL and it is the one connection state with an
// action attached, which is why it is a card in the flow and not the corner
// glyph beside it: a glyph cannot be pressed and a person looking at a dead
// tab wants the way back, not a diagnosis. It is the same shape the
// integration notice already uses for the same reason (StatusCard, placed in
// the pane rather than overlaid), and it takes the EDITOR's place — which is
// free, because a lost session hides the editor anyway. The scrollback above
// it stays readable in full; not losing it is why the tab was never closed.
//
// Reconnecting here opens a NEW shell, not a resumed connection. This card is
// for a session the backend has authoritatively declared absent: the helper no
// longer holds it, so the shell is gone, while a process it had detached may
// still be running. A session whose attach merely failed is the OTHER card
// (mountAttachOffer): its helper-owned shell may still be there, so it offers
// a retry of the same binding rather than a replacement.
//
// AND IT IS OFFERED ONLY FOR `lost`. A host that has merely stopped answering
// gets no button: the session may still be running on the far side, and
// pressing this would kill work that was never in danger.

import { Show, createSignal } from 'solid-js'
import { render } from 'solid-js/web'

import { Button } from './ui/button'
import { Spinner } from './ui/spinner'
import { StatusCard } from './ui/status-card'
import type { SessionAttachment } from './ipc'

export interface ReconnectOfferProps {
  /** Null while the pane has a session; the endpoint's name when it is lost. */
  host: string
  /** True while an attempt is in flight. */
  attempting: boolean
  /** How many automatic attempts have been spent, when any were. */
  attempt?: { spent: number; of: number }
  onReconnect: () => void
}

/** The values rather than the props object, so every read happens in the JSX
 *  below where Solid can track it. A helper handed `props` reads them outside
 *  a tracked scope and the card would keep its first sentence for good — which
 *  for this card means saying "Reconnecting…" after the attempt is over. */
function title(attempting: boolean, host: string): string {
  if (attempting) return 'Reconnecting…'
  return host ? `The connection to ${host} is gone` : 'The connection is gone'
}

function description(
  attempting: boolean,
  attempt: { spent: number; of: number } | undefined,
  host: string,
): string {
  if (attempting) {
    return attempt
      ? `Attempt ${attempt.spent} of ${attempt.of}.`
      : 'Opening a new shell at the same endpoint.'
  }
  const spent = attempt && attempt.spent > 0 ? 'Automatic attempts are spent. ' : ''
  const fate = host
    ? `Reconnecting opens a NEW shell at the same endpoint — anything the old one was running may still be going on ${host}.`
    : 'Reconnecting opens a NEW shell. The old session is gone; anything it had started as a separate process may still be running.'
  return `${spent}${fate} What it printed stays above.`
}

function ReconnectOffer(props: ReconnectOfferProps) {
  return (
    <StatusCard
      tone="danger"
      title={title(props.attempting, props.host)}
      description={description(props.attempting, props.attempt, props.host)}
      action={
        <Show when={!props.attempting} fallback={<Spinner label="Reconnecting" size="sm" />}>
          <Button variant="primary" onClick={() => props.onReconnect()}>
            Reconnect
          </Button>
        </Show>
      }
    />
  )
}

export interface ReconnectOfferHandle {
  set(props: ReconnectOfferProps): void
  dispose(): void
}

/** Mount the offer at the tail of the pane, where the editor would be. */
export function mountReconnectOffer(
  target: HTMLElement,
  initial: ReconnectOfferProps,
): ReconnectOfferHandle {
  const host = document.createElement('div')
  host.className = 'nocx-reconnect-offer'
  target.appendChild(host)
  let current = initial
  let dispose = render(() => <ReconnectOffer {...current} />, host)
  return {
    set(next: ReconnectOfferProps) {
      current = next
      dispose()
      dispose = render(() => <ReconnectOffer {...current} />, host)
    },
    dispose() {
      dispose()
      host.remove()
    },
  }
}

export interface AttachOfferHandle {
  set(state: SessionAttachment): void
  dispose(): void
}

/** A failed attach is not an ended shell. Retry only that existing binding. */
export function mountAttachOffer(
  target: HTMLElement,
  initial: SessionAttachment,
  onRetry: () => void,
): AttachOfferHandle {
  const host = document.createElement('div')
  host.className = 'nocx-attach-offer'
  target.appendChild(host)
  const [state, setState] = createSignal(initial)
  const explanation = () => {
    const current = state()
    return current.kind === 'unavailable'
      ? current.message
      : 'Input is paused while the existing session is being attached. No new shell is opened.'
  }
  const dispose = render(
    () => (
      <StatusCard
        tone="warning"
        title={
          state().kind === 'attaching' ? 'Restoring this session…' : 'This session is not attached'
        }
        description={explanation()}
        action={
          <Show
            when={state().kind === 'unavailable'}
            fallback={<Spinner label="Attaching" size="sm" />}
          >
            <Button variant="primary" onClick={onRetry}>
              Retry attachment
            </Button>
          </Show>
        }
      />
    ),
    host,
  )
  return {
    set: setState,
    dispose() {
      dispose()
      host.remove()
    },
  }
}
