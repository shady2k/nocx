// Dialog RPC client — typed methods for the dialog.* control-plane methods.
// Sibling of VaultClient/ProfileClient over the same Dispatcher.
//
// dialog.* is the renderer's path to the native platform dialogs: the
// renderer cannot call the Wails runtime directly (AD-1), so it asks the
// backend, which owns the Wails context. The backend may report the method
// unavailable (-32601) — the dev-web harness has no Wails at all — and the
// caller then degrades rather than failing.

import type { Dispatcher } from './dispatcher'
import type { DialogOpenFile } from './generated/dialog.openFile'
import type { DialogOpenDirectory } from './generated/dialog.openDirectory'

export class DialogClient {
  constructor(private dispatcher: Dispatcher) {}

  /** Open the native file picker. Resolves to the chosen ABSOLUTE path, or
   *  an empty path when the user cancelled. Rejects when no native runtime
   *  exists — the surface must degrade to typing the path by hand. */
  openFileDialog(): Promise<DialogOpenFile> {
    return this.dispatcher.call('dialog.openFile', {})
  }

  /** Open the native folder picker for the sandboxed-shell workspace
   *  (ADR-0019 §3.2). Resolves to the chosen ABSOLUTE directory, or an
   *  empty path when the user cancelled (a no-op for the action). */
  openDirectoryDialog(): Promise<DialogOpenDirectory> {
    return this.dispatcher.call('dialog.openDirectory', {})
  }
}
