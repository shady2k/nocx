/**
 * uistate-client — the renderer's end of the UI-state document (ADR-0048).
 *
 * UI state is what the app must remember WITHOUT being asked: the sidebar's
 * collapse, its active view, its width, and which tab was in front. It is not
 * a setting — a setting is something a user deliberately chooses — so it does
 * not go through the settings pipeline, does not appear on a Settings page and
 * never marks a section Modified. And it does not go into `localStorage`
 * either: localStorage may not carry facts.
 *
 * ## Why this client holds a copy
 *
 * `uistate.set` takes the whole renderer half, because an untyped
 * `(key, value)` setter is the shape ADR-0011 removed from `internal/config`.
 * But a drag knows only the width and a collapse knows only the collapse, so
 * something has to merge a change into the rest.
 *
 * The copy here is a MIRROR, not a second owner: it is seeded by `load()` and
 * replaced by whatever `uistate.set` answers with, and the backend answers
 * with what it stored rather than with what it was sent. So when the store
 * clamps a width, the mirror learns the clamped value on the same round trip
 * — which is the defect the contracts directory exists to prevent, in the one
 * place this feature could have reproduced it.
 */

import type { Dispatcher } from './dispatcher'
import type { UIState, Sidebar } from './generated/uistate'

export type { UIState }

/** What a caller may change in one go. Anything omitted keeps its value. */
export interface UIStatePatch {
  sidebar?: Partial<Sidebar>
  activeTab?: string
}

/** The declared defaults, painted before the first `load()` answers and used
 *  whenever the backend cannot be reached. They mirror the Go side's
 *  defaults; the Go side is authoritative and overwrites these the moment it
 *  replies. */
const DEFAULT_UI_STATE: UIState = {
  sidebar: { collapsed: false, activeViewId: '', width: 240 },
  activeTab: '',
}

export class UIStateClient {
  private current: UIState = structuredClone(DEFAULT_UI_STATE)

  constructor(private dispatcher: Dispatcher) {}

  /** The last state the backend confirmed. Synchronous, so the composition
   *  root can hand it to components that mount before any further round trip. */
  get state(): UIState {
    return this.current
  }

  /** Read the document. A failure leaves the declared defaults in place — an
   *  unreachable backend costs the user their layout, never their launch. */
  async load(): Promise<UIState> {
    try {
      this.current = await this.dispatcher.call<UIState>('uistate.get', {})
    } catch {
      // Defaults stand. Deliberately silent: there is nothing on screen
      // promising this succeeded, and nothing a user could do about it.
    }
    return this.current
  }

  /** Merge a change and write it. Resolves with the state as the backend now
   *  holds it, which is not always what was sent. Rejects only so a caller
   *  that wants to warn can; the width controller's seam does exactly that. */
  async save(patch: UIStatePatch): Promise<UIState> {
    const next: UIState = {
      sidebar: { ...this.current.sidebar, ...patch.sidebar },
      activeTab: patch.activeTab ?? this.current.activeTab,
    }
    const stored = await this.dispatcher.call<UIState>('uistate.set', next)
    this.current = stored
    return this.current
  }
}
