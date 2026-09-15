// CommandBlockFrame — the passive kit shell around a command block's header
// (decision record 2026-09-15 §1.5/§1.7, §4 task B). It owns DOM structure
// for the header's grid — a meta line, the command/question line beside it,
// and a status region that reads even with the command line once the block
// has settled and spans both lines, centered, while work is in progress —
// and the `ui-command-block-frame` identity that carries that layout in
// styles/components/command-block-frame.css.
//
// It owns NONE of the block's lifecycle, record or selection state — those
// stay in scrollback/blocks.ts (BlockManager, settleBlockOutcome, the
// BLOCK_KIND_RULES table), which is the one place that decides WHAT goes
// into these slots and WHEN. This module only builds the box: the existing
// `.cmd-header-meta` and `.cmd-header-right` hooks every caller and e2e spec
// already assumes keep their class names and their descendant-selector
// reachability (`:scope > .cmd-header .cmd-header-right`, …) — only the
// status region's DOM PARENT changed, from living inside the meta row to
// standing beside it as the header's own grid child, which is what lets it
// span both text lines while running without also widening row one alone.
//
// Vanilla-emitted, like Meta and PromptContext (ui/README): the terminal
// screen that uses it is imperative DOM (ADR-0012), built once per block.

export interface CommandBlockFrameSlots {
  /** The header element — `.cmd-header`, unchanged identity. */
  readonly header: HTMLElement
  /** Row 1: prompt/badge line — existing `.cmd-header-meta` hook. The
   *  caller appends its own content (the author badge, PromptContext). */
  readonly metaRow: HTMLElement
  /** The status region — existing `.cmd-header-right` hook. The caller
   *  appends the spinner, duration/terminal Metas, Stop and the ⋮ into
   *  this; the frame decides only where the GROUP sits, never what fills
   *  it (BLOCK_KIND_RULES is the one owner of that). */
  readonly status: HTMLElement
}

/**
 * Build the header's three grid children — `metaRow`, the caller's own
 * command/question row (appended directly to `header`, since the kind's
 * grammar decides whether that is a highlighted `.cmd-header-command` or a
 * bare `.cmd-header-text` — BLOCK_KIND_RULES, not this module), and
 * `status`. CSS places all three (command-block-frame.css); nothing here
 * decides a pixel.
 */
export function createCommandBlockFrame(): CommandBlockFrameSlots {
  const header = document.createElement('div')
  header.className = 'cmd-header ui-command-block-frame'

  const metaRow = document.createElement('div')
  metaRow.className = 'cmd-header-meta'

  const status = document.createElement('div')
  status.className = 'cmd-header-right'

  return { header, metaRow, status }
}

/**
 * Mark the header's status region as spanning BOTH text lines, centered,
 * rather than sitting even with the command line alone (spec §1.7): a
 * running command's spinner + `Running · 12.4s` + Stop, or the ask kind's
 * own "thinking" word share this one shape (AD-8 — one owner for "this
 * block is in progress"). Set once, structurally, by whoever builds or
 * settles the header — never derived from which chips happen to be inside
 * the region, so an empty status group and a full one behave alike.
 */
export function setHeaderInProgress(header: HTMLElement, inProgress: boolean): void {
  if (inProgress) header.dataset.headerProgress = 'true'
  else delete header.dataset.headerProgress
}
