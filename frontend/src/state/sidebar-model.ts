/**
 * sidebar-model — framework‑neutral sidebar state with named transitions.
 *
 * Derived from: frontend/src/sidebar.ts
 *   SidebarImpl._collapsed → .collapsed  (line 45)
 *   SidebarImpl._activeViewId → .activeViewId  (line 46)
 *
 * Authority:
 *   Collapsed    → sidebar component (keyboard shortcut Ctrl/Cmd+B, icon click)
 *   Active view  → sidebar component (icon click)
 *   Persistence  → SidebarPersistence, over the UI-state document
 *                  (ADR-0048) — not modeled here
 *
 * Terminal render state is NOT modeled (AD-6).  The sidebar never touches it.
 */

// ── Types ──────────────────────────────────────────────────────────────────

/**
 * Sidebar state — the activity bar's collapse state and active-view selection.
 *
 * Derived from: `SidebarImpl` class (sidebar.ts:44-154)
 *   SidebarImpl._collapsed (line 45)
 *   SidebarImpl._activeViewId (line 46)
 */
export interface SidebarState {
  /** True when the wide panel is hidden. The activity bar never hides. */
  readonly collapsed: boolean
  /** The selected view — retained while collapsed, so re-opening restores it. */
  readonly activeViewId: string | null
}

// ── Factory ─────────────────────────────────────────────────────────────────

/** Create sidebar state with the first view active and panel open. */
export function createSidebarState(initialViewId: string = ''): SidebarState {
  return { collapsed: false, activeViewId: initialViewId }
}

// ── Pure transition functions ───────────────────────────────────────────────

/**
 * Toggle the sidebar between collapsed and expanded.
 *
 * Authority: sidebar component (Ctrl/Cmd+B, icon click when active view shown).
 *
 * Derived from: SidebarImpl.toggle (sidebar.ts:120-124)
 */
export function toggleSidebar(state: SidebarState): SidebarState {
  return { ...state, collapsed: !state.collapsed }
}

/**
 * Set the active view.  If the view is already active and the sidebar is not
 * collapsed, the sidebar toggles collapsed (VS Code behaviour: clicking the
 * active view's icon closes the panel).  If a different view is clicked while
 * collapsed, the sidebar expands (matching SidebarImpl._activate).
 *
 * Authority: sidebar component (icon click).
 *
 * Derived from: SidebarImpl._activate (sidebar.ts:126-136)
 */
export function setActiveView(state: SidebarState, viewId: string): SidebarState {
  if (viewId === state.activeViewId && !state.collapsed) {
    // Clicking the active view's icon closes the panel.
    return { ...state, collapsed: true }
  }
  if (viewId !== state.activeViewId && state.collapsed) {
    // Switching to a different view while collapsed expands the panel
    // (matches SidebarImpl._activate behaviour).
    return { ...state, activeViewId: viewId, collapsed: false }
  }
  return { ...state, activeViewId: viewId }
}

/**
 * Collapse the sidebar unconditionally.
 *
 * Authority: tab activation (panel closes when content below gains focus).
 */
export function collapseSidebar(state: SidebarState): SidebarState {
  if (state.collapsed) return state
  return { ...state, collapsed: true }
}
