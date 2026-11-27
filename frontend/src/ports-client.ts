// The Ports panel reads discovery and the forwarding ledger over the control plane.

import type { TunnelListResult } from './generated/tunnel.list'
import type { Dispatcher } from './dispatcher'
import type { PortsStatusResult } from './generated/ports.status'
import type { PortsSampleResult } from './generated/ports.sample'
import type { PortsPauseResult } from './generated/ports.pause'
import type { PortsVisibleResult } from './generated/ports.visible'

export class PortsClient {
  constructor(private dispatcher: Dispatcher) {}

  listForwards(): Promise<TunnelListResult> {
    return this.dispatcher.call<TunnelListResult>('tunnel.list', {})
  }

  /** The discovery status for one pane. */
  status(paneId: string): Promise<PortsStatusResult> {
    return this.dispatcher.call<PortsStatusResult>('ports.status', { paneId })
  }

  /** Sample immediately and return the current status. */
  sample(paneId: string): Promise<PortsSampleResult> {
    return this.dispatcher.call<PortsSampleResult>('ports.sample', { paneId })
  }

  /** The user's Pause/Resume control. */
  pause(paneId: string, paused: boolean): Promise<PortsPauseResult> {
    return this.dispatcher.call<PortsPauseResult>('ports.pause', { paneId, paused })
  }

  /** The panel watcher's visibility: periodic sampling runs only while a
   *  watcher is visible and nothing is paused. */
  visible(paneId: string, visible: boolean): Promise<PortsVisibleResult> {
    return this.dispatcher.call<PortsVisibleResult>('ports.visible', { paneId, visible })
  }
}
