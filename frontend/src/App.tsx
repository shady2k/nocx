import type { Component } from 'solid-js'
import { ToastHost } from './ui/toast'

/**
 * App — single Solid root that owns the shell layout and provides empty hosts
 * for every part of the imperative chrome:
 *
 *   #tabbar             — TabStripBase mounts its Solid fragment here (horizontal)
 *   #vertical-tabstrip  — TabStripBase mounts here (vertical)
 *   #panes              — PaneManager appends tab panes here (Solid MUST never
 *                         render children beneath, key, or remount this element)
 *   #sidebar            — sidebar panel, managed by SidebarSolid
 *   #activitybar        — mountSidebar renders SidebarSolid here
 *
 * #tabbar stays present in both placements as the Wails drag region.
 * #body is a flex row and lists those four in that order, leading edge to
 * trailing edge — see the comment on #sidebar below for why the rail is last.
 *
 * Per-tab surfaces (settings, connections, export) keep their own Solid roots
 * mounted into panes by the PaneContent seam — converting those is not this
 * bead's scope.
 *
 * No reactive state: the shell skeleton is static. All lifecycle and wiring
 * lives in the imperative bootstrap in main.ts.
 */
const App: Component = () => {
  return (
    <>
      <div id="tabbar" class="tabbar" />
      <div id="workspace">
        <div id="body">
          {/* The vertical tab strip lists the panes, so it sits next to them,
              at the window's LEADING edge. */}
          <div id="vertical-tabstrip" />
          <div id="panes" />
          {/* The panel and its rail are LAST — the window's TRAILING edge — and
              in both tab placements, unconditionally.

              Not because they are app-level chrome. That was the old reason and
              it was wrong: three of the six panel views render for whatever tab
              is in front (Files, Ports and Git read activeOrigin /
              activeProfileId), and the other three are window-scoped. A
              window-scoped view is equally at home on either edge, and a
              tab-scoped one placed BEFORE the thing that selects the tab is
              not. So this edge is correct or neutral for all six and the
              leading edge is wrong for three — the asymmetry is the reason, not
              a generalisation about all of them, which is what the sentence
              here used to be.

              Only vertical placement had the defect: #tabbar is a sibling of
              #workspace spanning the width above everything, so with horizontal
              tabs the strip already dominated. The rail moves in BOTH anyway,
              because chrome that changes sides when a display preference
              changes costs the user more than the ordering it buys.

              The trade is real and taken deliberately: traversal now reaches
              the panel's content before the toolbar that chooses it. Painting
              the rail rightmost with `order` while keeping it early in the DOM
              would make DOM order disagree with visual order, which is the
              worse defect. See
              .internal/specs/2026-08-29-activity-bar-right-edge-design.md §5. */}
          <div id="sidebar" />
          <div id="activitybar" />
        </div>
      </div>
      {/* The notification area. Mounted here, once, because a toast raised by a
          per-tab surface has to render above the whole window rather than inside
          the pane that raised it — and because two hosts would show every toast
          twice. It is fixed and empty until something is raised, so it costs no
          layout. */}
      <ToastHost />
    </>
  )
}

export default App
