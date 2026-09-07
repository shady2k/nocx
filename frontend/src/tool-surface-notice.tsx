/**
 * The worker tool-surface notice (nocx-rowqt.2.3): a third surface beside
 * shell integration and reclaimed-pane recovery. It is not folded into either
 * existing notice because its fact, vocabulary and lifetime belong to the
 * launch-owned endpoint, not to shell takeover or recovered output.
 *
 * This surface places the kit StatusCard above the terminal. It does not make
 * a second card component or write into the pty while an agent owns the pane.
 */

import { render } from 'solid-js/web'
import type { SessionToolSurfaceChanged } from './generated/session.toolSurfaceChanged'
import { IconButton } from './ui/icon-button'
import { StatusCard } from './ui/status-card'

export interface ToolSurfaceNoticeProps {
  fact: SessionToolSurfaceChanged
  onDismiss: () => void
}

export function mountToolSurfaceNotice(
  target: HTMLElement,
  props: ToolSurfaceNoticeProps,
): () => void {
  const host = document.createElement('div')
  host.className = 'nocx-tool-surface-notice'
  target.insertBefore(host, target.firstChild)
  const dispose = render(
    () => (
      <StatusCard
        tone="danger"
        title="Worker tools unavailable"
        description={`The nocx worker tool surface is unavailable: ${props.fact.reason ?? 'the endpoint did not answer'}.`}
        action={
          <IconButton ariaLabel="Dismiss" size="sm" onClick={props.onDismiss}>
            {'×'}
          </IconButton>
        }
      />
    ),
    host,
  )
  return () => {
    dispose()
    host.remove()
  }
}
