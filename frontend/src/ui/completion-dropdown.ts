// Completion dropdown — the completion VARIANT of the kit's FloatingPanel
// (ui/README table): `ui-floating-panel[data-variant='completion']`. The
// controller owns the state machine and the keyboard; this module is the
// completion shape of a pure view — it maps candidates to rows, renders
// them through the shared panel, and reports hover/pick, nothing else.
//
// What stays HERE (the variant's) and what moved to the primitive:
//   - here: the kind word and source badge per row, the history-section
//     decision (groups are stamped only in a MIXED list), the empty-reason
//     row, and the hint wording;
//   - the primitive: the shell (anchoring, content-sized width, max-height
//     and scrolling), the row list and row overflow, the group caption, the
//     footer, and the match highlight.
//
// Identity family: the shared `ui-floating-panel` + `__*` parts (one CSS
// file, floating-panel.css); this variant adds no root identity of its own.
// Rows reuse the kit's `ui-collection-row` identity for the selected
// variance; the source and kind captions are kit `ui-badge` tones. The
// evidence column (design §8.10) is rendered but never inserted —
// `displayText` is what a row shows, `insertText` is what the controller
// applies.

import { FloatingPanel, type FloatingPanelRow } from './floating-panel'

/** One row the kit draws. Deliberately a display subset of the domain
 *  Candidate (design §8.9): the kit must not depend back on the app, so the
 *  controller maps candidates to rows before showing them — `insertText`
 *  and the replacement range never cross into the kit. */
export interface CompletionRow {
  readonly id: string
  readonly displayText: string
  readonly matchRanges: Array<{ from: number; to: number }>
  readonly source: string
  /** The filesystem kind of a path row — rendered as its type word
   *  (`Directory` / `File`), the answer to "how do I tell a file from a
   *  folder". Absent rows render no kind badge. */
  readonly kind?: 'directory' | 'file'
}

export interface CompletionDropdownCallbacks {
  /** A row was hovered (mouse) — the controller moves the selection. */
  onHover(index: number): void
  /** A row was clicked — the controller accepts it. */
  onPick(index: number): void
}

const SOURCE_LABEL: Record<string, string> = {
  command: 'command',
  history: 'history',
  path: 'path',
  host: 'host',
  snippet: 'snippet',
  function: 'function',
}

/** The type word for a path row — `Directory` / `File` (the owner's ask:
 *  "how do I tell a file from a folder"). A kit badge, never a repaint. */
const KIND_LABEL: Record<string, string> = {
  directory: 'Directory',
  file: 'File',
}

/** The group caption above the history rows in a mixed list — one
 *  vocabulary with the source badge: the badge names each row, the caption
 *  names the section. */
const GROUP_LABEL = 'History'

/** The key hints the footer carries for a selectable list.
 *
 *  Two keys take the selected row and they do NOT do the same thing, which
 *  is why they are named apart: Enter takes it and stops, Right takes it and
 *  keeps going — a directory taken with Right shows what is inside it, so a
 *  path is walked with one key. They were one call behind two words once. */
const FOOTER_HINTS = [
  '↵ to insert',
  'tab ↹ to cycle',
  '↑ ↓ to navigate',
  '→ to accept and continue',
  'esc to dismiss',
] as const

export class CompletionDropdown {
  readonly root: HTMLElement
  private readonly panel: FloatingPanel

  constructor(callbacks: CompletionDropdownCallbacks) {
    this.panel = new FloatingPanel({
      variant: 'completion',
      role: 'listbox',
      ariaLabel: 'completions',
      callbacks,
    })
    this.root = this.panel.root
  }

  get isOpen(): boolean {
    return this.panel.isOpen
  }

  /** Mount the panel as a child of the editor's root, so it floats above
   *  the editor (the root is position: relative). */
  mount(container: HTMLElement): void {
    this.panel.mount(container)
  }

  /**
   * Render the current candidate list. Selected index is the controller's
   * decision (it owns the state machine); the view only draws it.
   * `anchorLeft` is the caret's x, in px relative to the panel's offset
   * parent (the editor root) — the panel opens at the caret, not at the
   * pane's edge; null keeps the kit's left-edge default.
   */
  show(rows: CompletionRow[], selectedIndex: number, anchorLeft?: number | null): void {
    // History rows are their own group, at the end: a path candidate
    // replaces the current TOKEN, a history candidate replaces the WHOLE
    // LINE, and a mixed list must say the two kinds apart (the owner's
    // "this suggestion looks strange" — a whole-line row in a list of path
    // rows). A pure-history list (no paths to separate from) needs no
    // caption — the VARIANT decides the section, the primitive renders it.
    const mixed =
      rows.some((r) => r.source !== 'history') && rows.some((r) => r.source === 'history')
    this.panel.show({
      rows: rows.map((r) => this.toPanelRow(r, mixed)),
      selectedIndex,
      anchorLeft,
      footer: FOOTER_HINTS,
    })
  }

  /**
   * The honest "nothing to choose" state: one non-selectable row naming why
   * (zero candidates is a state the product shows, never silence). No
   * footer — the hints describe a selectable list, and this row has nothing
   * to insert, cycle or navigate.
   */
  showEmpty(message: string, anchorLeft?: number | null): void {
    this.panel.showEmpty(message, anchorLeft)
  }

  /** Close the panel and drop its rows. */
  hide(): void {
    this.panel.hide()
  }

  destroy(): void {
    this.panel.destroy()
  }

  private toPanelRow(r: CompletionRow, mixed: boolean): FloatingPanelRow {
    const actions: Node[] = []
    if (r.kind) {
      const kind = document.createElement('span')
      kind.className = 'ui-badge ui-floating-panel__kind'
      kind.dataset.tone = 'neutral'
      kind.textContent = KIND_LABEL[r.kind] ?? r.kind
      actions.push(kind)
    }
    const badge = document.createElement('span')
    badge.className = 'ui-badge ui-floating-panel__source'
    badge.dataset.tone = 'neutral'
    badge.textContent = SOURCE_LABEL[r.source] ?? r.source
    actions.push(badge)
    return {
      id: r.id,
      displayText: r.displayText,
      matchRanges: r.matchRanges,
      actions,
      group: mixed && r.source === 'history' ? GROUP_LABEL : undefined,
    }
  }
}
