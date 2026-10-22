/**
 * window-geometry — the renderer's relay for the window half of the
 * UI-state document (ADR-0098).
 *
 * The shell owns the native window. The coordinator owns the document.
 * No process holds both, and the WebSocket belongs to this renderer — so
 * the saved geometry travels out through a Wails binding and every live
 * sample travels back as a notification, with this module deciding
 * nothing on either leg (AD-3: the shell performs, the store folds).
 *
 * The ordering is load-bearing: the restore binding is what starts the
 * shell's sampler, so a launch can never record its own opening over the
 * geometry being restored. A client with no native host — a plain
 * browser, the e2e suite — gets neither half: no restore, no samples,
 * and the window half of the document stays as it was.
 */

import { Events } from '@wailsio/runtime'
import { HostRestoreWindow } from '../bindings/github.com/shady2k/nocx/wailsapp'
import { bindingReachable } from './wails-runtime'
import type { Dispatcher } from './dispatcher'
import type { UIStateWindow } from './generated/uistate.window'

/** The Wails event the shell emits whenever its window's geometry
 *  CHANGES — not per tick, so a still window costs nothing. */
export const WINDOW_SAMPLED_EVENT = 'nocx:windowSampled'

/** The binding this relay needs. Used for the reachability probe by full
 *  name, the way client-host probes its own. */
const RESTORE_BINDING = 'main.WailsApp.HostRestoreWindow'

/** The native effects this relay performs, as a seam. The default binds
 *  the generated Wails bindings; a test binds doubles and needs no
 *  runtime. */
export interface GeometryBindings {
  restoreWindow(saved: UIStateWindow): Promise<void>
}

/** The Wails event seam, narrow for the same reason as client-host's. */
export interface GeometryEvents {
  on(name: string, handler: (data: unknown) => void): () => void
}

const wailsGeometryBindings: GeometryBindings = {
  restoreWindow: (saved) =>
    HostRestoreWindow(
      saved.width,
      saved.height,
      saved.x,
      saved.y,
      saved.maximised,
      saved.fullScreen,
      saved.displays,
    ),
}

const wailsGeometryEvents: GeometryEvents = {
  on: (name, handler) => Events.On(name, (ev) => handler(ev)),
}

/**
 * Mount the window-geometry relay. Reads the saved geometry once, hands
 * it to the shell to apply (which is what starts the shell's sampler),
 * and relays every sample the shell emits afterwards. Returns the
 * unsubscribe function.
 *
 * A failure on any leg leaves the window as it opened: an unreachable
 * backend or a missing binding costs the user their saved geometry for
 * this launch, never the launch itself — the same degrade the layout
 * half lives by (ADR-0048 §5).
 */
export function mountWindowGeometry(
  dispatcher: Dispatcher,
  bindings: GeometryBindings = wailsGeometryBindings,
  events: GeometryEvents = wailsGeometryEvents,
): () => void {
  void restoreSavedGeometry(dispatcher, bindings)
  return events.on(WINDOW_SAMPLED_EVENT, (data) => {
    const sample = sampledWindow(data)
    if (!sample) return
    dispatcher.notify('uistate.window.observe', sample)
  })
}

/** Read the saved geometry and hand it to the shell. Skipped whole when
 *  this client has no native host: a browser window is not the window of
 *  record, and calling the binding would only start a sampler with
 *  nothing true to say. */
async function restoreSavedGeometry(
  dispatcher: Dispatcher,
  bindings: GeometryBindings,
): Promise<void> {
  try {
    const saved = await dispatcher.call<UIStateWindow>('uistate.window.get', {})
    if (!bindingReachable(RESTORE_BINDING)) return
    // Called even when nothing was ever saved: the binding is what starts
    // the shell's sampler, and a first launch is recorded the same way as
    // every later one.
    await bindings.restoreWindow(saved)
  } catch {
    // Defaults stand. Deliberately silent, for the same reason as the
    // layout half's load(): there is nothing on screen promising this
    // succeeded, and the next launch retries.
  }
}

/** The Wails event payload is whatever the shell emitted. v3 wraps it in
 *  a WailsEvent whose `data` is the emitted value — and a single emitted
 *  value may arrive either bare or as a one-element array, so both are
 *  read rather than one being assumed. Anything that does not name a
 *  whole sample is ignored: a sample with no size is not a sample. */
function sampledWindow(data: unknown): UIStateWindow | null {
  const payload: unknown = (data as { data?: unknown } | null)?.data ?? data
  const candidate: unknown = Array.isArray(payload) ? (payload as unknown[])[0] : payload
  if (typeof candidate !== 'object' || candidate === null) return null
  const w = candidate as Record<string, unknown>
  if (
    typeof w.width !== 'number' ||
    typeof w.height !== 'number' ||
    typeof w.x !== 'number' ||
    typeof w.y !== 'number' ||
    typeof w.maximised !== 'boolean' ||
    typeof w.fullScreen !== 'boolean' ||
    typeof w.displays !== 'string'
  ) {
    return null
  }
  if (w.width <= 0 || w.height <= 0) return null
  return { ...candidate } as UIStateWindow
}
