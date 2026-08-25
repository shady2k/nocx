// ═══════════════════════════════════════════════════════════════════════════
// SandboxStatisticsContent — wraps the Solid sandbox statistics component
// as a PaneContent. Singleton, non-restorable.
// ═══════════════════════════════════════════════════════════════════════════

import { createComponent } from 'solid-js'
import { render } from 'solid-js/web'
import { SolidPaneContent, type PaneHost } from './solid-pane-content'
import type { SurfaceType, SingletonKey } from './pane-content'
import {
  SandboxStatistics,
  type SandboxStatisticsClient,
  type SandboxStatisticsDeps,
} from './sandbox-statistics'

// ── Registered surface constants ──────────────────────────────────────

export const SURFACE_SANDBOX_STATISTICS: SurfaceType = 'nocx.sandbox-statistics' as SurfaceType
export const SINGLETON_SANDBOX_STATISTICS: SingletonKey = 'nocx.sandbox-statistics' as SingletonKey

// ── SandboxStatisticsContent ──────────────────────────────────────────

export class SandboxStatisticsContent extends SolidPaneContent {
  constructor(
    private readonly client: SandboxStatisticsClient,
    private readonly deps: SandboxStatisticsDeps,
  ) {
    super()
  }

  renderContent(root: HTMLElement): () => void {
    return render(
      () =>
        createComponent(SandboxStatistics, {
          client: this.client,
          deps: this.deps,
        }),
      root,
    )
  }

  async mount(target: HTMLElement, host: PaneHost, signal: AbortSignal): Promise<void> {
    if (this._disposed || this._hostElement) return
    if (signal.aborted) return

    host.setTitle('Sandbox')
    await super.mount(target, host, signal)
  }
}
