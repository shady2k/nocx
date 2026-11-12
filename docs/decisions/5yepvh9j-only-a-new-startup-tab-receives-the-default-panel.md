# ADR-5yepvh9j — Only a new startup tab receives the default panel

- **Status:** Accepted (2026-11-12, `nocx-peg8e.1.2`)
- **Date:** 2026-11-12
- **Supersedes:** ADR-x7cvh8e1's startup-seeding exception only.

## Context

ADR-x7cvh8e1 stores the selected panel and collapse bit on each tab's layout
row. Its exception seeds the tab the window opens with once per page load.
That includes a restored tab whose empty panel is a valid saved choice:
startup replaces it with Files and writes over the tab's row. The existing
restart check opens on Ports, so it never exercises that exception.

## Decision

Only a tab **created by startup** receives the default panel. A restored tab
always uses its stored pair, including an empty view. Ordinary new tabs still
start with no panel. A clean start creates a new initial tab and receives the
default, just like an empty layout.

PaneManager already decides whether startup adopts a tab or creates one. It
accepts the composition root's default panel at `openInitialPane` and applies
it only in the latter case, through the existing tab-panel writer before
activation. The composition root supplies Files; it no longer infers a fresh
tab from an empty view or tracks the first pane element for defaulting.

The layout row remains the only durable owner. No settings or UI-state fields
change, and no state inside a panel is retained by this decision.

## Verification

`e2e/panel-per-tab.spec.ts` restarts backend and renderer with an empty panel
on the opening tab and Ports on its neighbour, in addition to the existing
collapsed-Files/Ports case. `panes-layout.test.ts` checks defaulting on a fresh
startup, a later tab, a restored empty tab and a clean start.
