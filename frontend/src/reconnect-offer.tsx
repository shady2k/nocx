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
// WHAT THE WORDS MUST NOT PROMISE. Reconnecting gives a NEW shell. A card that
// said "Reconnect" and nothing else would let a person read their scrollback as
// one continuous session and be wrong about the machine on the other end, so
// the description says what will actually happen — and what that IS differs by
// pane. For a remote session the far host is alive and whatever the old shell
// was running may still be, with its own cwd and its own descriptors. For a
// local one there is no far host: the shell was the backend's child and went
// with it, so only something that had already detached can have survived.
// Saying the first to a local pane invents a machine on the other end.
//
// AND IT IS OFFERED ONLY FOR `lost`. A host that has merely stopped answering
// gets no button: the session may still be running on the far side, and
// pressing this would kill work that was never in danger.

import { Show } from 'solid-js'
import { render } from 'solid-js/web'

import { Button } from './ui/button'
import { Spinner } from './ui/spinner'
import { StatusCard } from './ui/status-card'

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
  // The honest sentence, and the reason this card has a description at all —
  // and it is a DIFFERENT sentence for the two cases, because the fate of the
  // old shell is different and this card exists to state exactly that.
  //
  // Remote: the far host is alive and the session ended between here and
  // there, so whatever it was running very likely still is, with its own cwd
  // and its own descriptors.
  //
  // Local: there is no far host. The shell was a child of the backend and the
  // PTY master closed when the backend went, so it took a SIGHUP with it —
  // what survives is only what had already detached itself. The remote
  // sentence was shown here too and promised a machine on the other end that
  // does not exist (nocx-ypbii; the owner read it after restarting a dev
  // stand, where the coordinator restarting IS the disconnection).
  const fate = host
    ? `Reconnecting opens a NEW shell at the same endpoint — anything the old one was running may still be going on ${host}.`
    : 'Reconnecting opens a NEW shell. The old one ended with the backend; anything it had detached may still be running.'
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
