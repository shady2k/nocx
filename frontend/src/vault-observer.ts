// VaultObserver — consumes vault.changed notifications.
//
// Modeled on SettingsObserver (settings-observer.ts). The notification
// carries the full VaultStatus shape, but the handler does NOT receive
// data — on any change the handler refetches the full snapshot from the
// backend. On reconnect the handler is also called so stale state is
// replaced.

import type { Dispatcher } from './dispatcher'

export type VaultInvalidationHandler = () => void

export class VaultObserver {
  private unsub: (() => void) | null = null
  private unsubConnect: (() => void) | null = null
  private active = false

  constructor(private dispatcher: Dispatcher) {}

  /** Start listening for vault.changed notifications. */
  start(handler: VaultInvalidationHandler): void {
    if (this.active) return
    this.active = true

    this.unsub = this.dispatcher.subscribe('vault.changed', (params: unknown) => {
      void params
      if (!this.active) return
      handler()
    })

    this.unsubConnect = this.dispatcher.onConnect(() => {
      if (!this.active) return
      handler()
    })
  }

  stop(): void {
    this.active = false
    this.unsub?.()
    this.unsub = null
    this.unsubConnect?.()
    this.unsubConnect = null
  }
}
