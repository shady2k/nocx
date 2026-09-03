// Footprint RPC client — typed methods for the shell.footprint.* control-
// plane methods (nocx-mlm7 P10, delivery-modes design §4.1/§9): the visible
// footprint of nocx's silent install, and the uninstall action.
//
// shell.footprint.status is READ-ONLY and never connects: the answer is the
// backend's installed fact (last observed on the host — the renderer never
// sees the observation mechanism), so the surface can show a host nocx can
// no longer reach, and lastObservedAt is "when nocx last saw it", never a
// claim about the host right now.
//
// shell.footprint.uninstall and shell.footprint.helperUninstall are
// offered only where a removableProfileId is present (a saved connection
// resolves to them) — an action that is valid from the state the user is
// in. The backend owns the dial; the renderer never sees an SSH client.
// The helper uninstall closes every running helper-hosted session through the
// daemon's session service before removing its tree and revokes machine
// consent, so the next status call stops listing the host.

import type { Dispatcher } from './dispatcher'
import type { ShellFootprintHelperUninstallResult } from './generated/shell.footprint.helperUninstall'
import type { ShellFootprintStatusResult } from './generated/shell.footprint.status'
import type { ShellFootprintUninstallResult } from './generated/shell.footprint.uninstall'

export class FootprintClient {
  constructor(private dispatcher: Dispatcher) {}

  /** Every destination nocx has an installed fact for: what was written,
   *  where (~/.nocx), when last seen, and which saved connection (if any)
   *  can remove it. Empty list = nothing ever observed installed. */
  status(): Promise<ShellFootprintStatusResult> {
    return this.dispatcher.call<ShellFootprintStatusResult>('shell.footprint.status', {})
  }

  /** Remove the integration bundle on the host a saved connection reaches.
   *  Only manifest-owned, unmodified files are removed; the result names
   *  both lists — removed and conflicts (files the user changed, left in
   *  place). */
  uninstall(profileId: string): Promise<ShellFootprintUninstallResult> {
    return this.dispatcher.call<ShellFootprintUninstallResult>('shell.footprint.uninstall', {
      profileId,
    })
  }

  /** Remove the remote helper on the host a saved connection reaches
   *  (remote-helper design D25): the whole ~/.nocx/helper tree goes —
   *  including directories an interrupted install left incomplete — and the
   *  row disappears from the listing. removed reports whether a tree
   *  existed at all; uninstalling a host with nothing installed is a
   *  no-op that succeeds. */
  helperUninstall(
    profileId: string,
    fingerprint: string,
    path: string,
  ): Promise<ShellFootprintHelperUninstallResult> {
    return this.dispatcher.call<ShellFootprintHelperUninstallResult>(
      'shell.footprint.helperUninstall',
      { profileId, fingerprint, path },
    )
  }
}
