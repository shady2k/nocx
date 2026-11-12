# ADR-5yepvh9j — Only an unchosen startup panel receives the default

- **Status:** Accepted (2026-11-12, `nocx-peg8e.1.2`)
- **Date:** 2026-11-12
- **Supersedes:** ADR-x7cvh8e1's startup-seeding exception only.

## Context

ADR-x7cvh8e1 stores the selected panel and collapse bit on each tab's layout
row. Seeding every restored empty view overwrites a valid no-panel choice.
Seeding only tabs created by the renderer fails in the opposite direction:
a fresh row can already exist when boot adopts it. In particular, the e2e
stand closes its last pane with a replacement before the next renderer boots.
That replacement has the store's default pair, empty view and expanded.

Closing a visible panel retains its view and sets collapsed to true. Thus an
empty view alone does not say whether the row has chosen a panel state.

## Decision

The existing layout pair distinguishes an **unchosen panel** (`view: ''`,
`collapsed: false`) from an **explicit no-panel choice** (`view: ''`,
`collapsed: true`). No additional field or persistence owner is needed.

Only the tab startup puts in front receives the default panel, and only if
its pair is unchosen. It does not matter whether startup created that tab or
adopted a backend row. The composition root supplies Files; PaneManager
records the default through the existing tab-panel writer before activation.

A tab the person opens later starts with the explicit no-panel pair, recorded
through that same writer. It remains closed when restored as the opening tab.
Closing Files retains Files and its collapse bit; Ports retains Ports. Neither
choice is replaced at startup. A clean start still receives the default.

The layout row remains the only durable owner. No settings or UI-state fields
change, and no state inside a panel is retained by this decision.

## Verification

`e2e/files.spec.ts` checks the shared stand's adopted cold-start tab opens on
Files. `e2e/panel-per-tab.spec.ts` restarts backend and renderer on both a
collapsed Files tab and an explicitly empty tab, with Ports on the neighbour.
`panes-layout.test.ts` checks defaulting on fresh and adopted unchosen startup
tabs, later tabs, an explicitly empty restored tab and a clean start.
