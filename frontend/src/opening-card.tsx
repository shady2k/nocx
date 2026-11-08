// The pane's own account of an open that is taking a while (nocx-q9od5).
//
// # Why it is in the PANE
//
// A first open of a host where the helper is not installed is mostly a 15.3 MB
// upload — measured on 2026-11-08 at 1.15 s over loopback and ~30 s on the
// e2e fixture's 0.5 MB/s link, against every other stage of the same open at
// 150 ms or less. The Operations surface has said what the install is doing
// since nocx-3an1s.2.10, with a Cancel, and the owner still reported a blank
// pane: they were looking at the pane, which is where a person waits. So the
// same facts are stated where the wait is felt.
//
// # What it states, and what it never invents
//
// The title is the OPEN's own name — the catalogue's "Opening your terminal" —
// until the backend reports work inside it, and then it is that work's title
// ("Copying the nocx helper"), the same words the Operations row uses, because
// they are one row's facts read by two surfaces. The description carries the
// byte count only when the backend reported one; before that it names the host
// and nothing else. A card that drew "0 B of 15.3 MB" from silence would
// report a transfer nobody had started.
//
// Cancel is not a second cancel: it is the call's own withdrawal
// (`rpc.cancel` for the id the dispatcher announced), so the tab closes, the
// renderer's call settles and the backend stops at its next chunk boundary —
// the same path the Operations row's Cancel takes.
//
// It takes the pane's surface while the pane has no grid to show, which is
// exactly the interval it exists for; the first frame retires it.

import { createSignal } from 'solid-js'
import { render } from 'solid-js/web'

import { Button } from './ui/button'
import { StatusCard } from './ui/status-card'
import { formatProgress } from './ui/format-bytes'
import { t } from './i18n/locale-model'

export interface OpeningCardState {
  /** The call's own title, or the open's when nothing inside it has spoken. */
  title: string
  /** The host, as the tab names it. Empty before a destination is known. */
  host: string
  /** What the backend reported about the work inside the call, when it has. */
  progress?: { done: number; total: number }
  /** Withdraw the open. */
  onCancel: () => void
}

export interface OpeningCardHandle {
  set(state: OpeningCardState): void
  dispose(): void
}

/** The description: the transfer's own numbers when there are any, and the
 *  destination otherwise. The host is never dropped — "3.2 MB of 15.3 MB"
 *  alone does not say to where. */
function description(state: OpeningCardState): string {
  const host = state.host
  const progress = state.progress
  const to = host === '' ? '' : ` ${t('opening.to', 'to {host}', { host })}`
  if (progress === undefined || progress.total <= 0) {
    return host === ''
      ? t('opening.waiting', 'Waiting for the remote shell.')
      : t('opening.connecting', 'Connecting to {host}.', { host })
  }
  const line = formatProgress({
    done: progress.done,
    total: progress.total,
    speedBytesPerSecond: null,
  })
  return `${line}${to}`
}

export function mountOpeningCard(
  target: HTMLElement,
  initial: OpeningCardState,
): OpeningCardHandle {
  const host = document.createElement('div')
  host.className = 'nocx-opening-card'
  // The pane's surface, not a page's: the card is the pane's state until the
  // pane has one of its own, and a spec finds it by this name.
  host.dataset.testid = 'pane-opening'
  target.appendChild(host)
  const [state, setState] = createSignal(initial)
  const dispose = render(
    () => (
      <StatusCard
        tone="neutral"
        title={state().title}
        description={description(state())}
        action={
          <Button variant="default" onClick={() => state().onCancel()}>
            {t('opening.cancel', 'Cancel')}
          </Button>
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
