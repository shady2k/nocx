/**
 * The pane's waiting state for exactly the `starting` interval (nocx-ui8q6.1).
 *
 * ADR-0024 decision 8 names the state this exists to prevent: "the user is
 * left at a suppressed prompt with raw input, which is the worst of both."
 * `starting` is the honest interval before the shell has proved itself — the
 * shell may already be mid-bootstrap, with its own prompt not yet visible and
 * not yet suppressed either — so a keystroke typed here has nowhere good to
 * land. This surface is the renderer's half of that: while the axis has not
 * answered, the terminal grid is hidden and this card stands in for it; the
 * keystroke drop itself lives in terminal-content.ts, at the one seam
 * keystrokes leave the pane through.
 *
 * `isAwaitingIntegration` in ./status is the ONE place that reads `starting`
 * off the fact (AD-8) — this module never re-derives it.
 *
 * Everything visible here is a kit component placed by this surface, exactly
 * like ../integration/notice.tsx: EmptyState and Spinner are both already the
 * kit's vocabulary for "nothing to show yet, and here is why" — a bespoke
 * waiting screen would be a second one. The identity class below positions it
 * in the pane's flex column and repaints nothing (frontend/src/ui/README.md).
 */

import { render } from 'solid-js/web'

import { EmptyState } from '../ui/empty-state'
import { Spinner } from '../ui/spinner'
import { INTEGRATION_STARTING_MESSAGE } from './status'

export function mountIntegrationWaiting(target: HTMLElement): () => void {
  const host = document.createElement('div')
  host.className = 'nocx-integration-waiting'
  target.insertBefore(host, target.firstChild)
  const dispose = render(
    () => (
      <EmptyState
        icon={<Spinner label={INTEGRATION_STARTING_MESSAGE} size="sm" />}
        title={INTEGRATION_STARTING_MESSAGE}
      />
    ),
    host,
  )
  return () => {
    dispose()
    host.remove()
  }
}
