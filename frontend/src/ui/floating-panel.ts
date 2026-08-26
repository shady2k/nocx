// FloatingPanel — the kit's floating-panel primitive (ui/README table).
// The shell every surface that floats above the command editor is a variant
// of: the completion dropdown (data-variant="completion") and the recall
// overlay (data-variant="recall"). Vanilla-emitted, like the variants it
// serves: the editor is a DOM surface, not a React tree.
//
// One identity family: `ui-floating-panel` + `__*` parts, one CSS file in
// styles/components/ (ADR-0013 §1). Rows reuse the kit's `ui-collection-row`
// identity for the selected variance; the variant supplies the evidence
// column (badges, timestamps) and its own chrome (header, detail, search).
//
// What the primitive owns — the sizing rules are ONE copy, never per-variant:
//   - anchoring: above the prompt, at the caret where that applies;
//   - width: content-sized between a floor and a ceiling, measured once per
//     list content, never per selection change;
//   - max-height and scrolling: a long list scrolls inside the panel;
//   - the row list, the group caption and the footer of key hints;
//   - overflow for a row too long for the ceiling: ellipsis, never a
//     clipped glyph (the owner's "repos/meshynet/graphify-ou…").
//
// The variants keep their structure (the recall's search field and coverage
// line, the completion's kind badges and reason row) and render it through
// `before` / `after` sections — the primitive slots them around the list.

/** How wide a row may make the panel, in px — the panel hugs its widest
 *  content and never spans the pane (the owner's "why full width?"). */
export const MAX_PANEL_WIDTH_PX = 640

/** The matched substring's class — handed to a row's own renderer so the
 *  highlight stays the panel's, wherever the text is built. */
const MATCH_CLASS = 'ui-floating-panel__match'

/** Floor: a single short row must not leave a sliver of a panel.
 *
 *  The floor is per variant because the two surfaces are read differently,
 *  not because their sizing rules differ — the rule (hug the content between
 *  a floor and the ceiling) is one copy. The completion dropdown sits at the
 *  caret and is glanced at, so a short list may be narrow. Recall is a
 *  BROWSING surface with a search field and a footer: sized to its rows it
 *  collapses to a sliver whenever the rows are short — and to nothing at all
 *  when there are no rows, where "no history yet" then reads as a broken
 *  panel rather than an empty one. */
export const MIN_PANEL_WIDTH_PX: Record<FloatingPanelVariant, number> = {
  completion: 320,
  recall: 560,
  // The secret picker sits between: a browsing surface with a filter and a
  // footer (wider than the completion's glance) whose rows are short names
  // (narrower than recall's command lines).
  secret: 480,
  grant: 360,
}

/** The surfaces that float over the editor — one shell, one layout per
 *  surface. The secret picker and grant list join the completion dropdown
 *  and recall overlay as variants of the same primitive. */
export type FloatingPanelVariant = 'completion' | 'recall' | 'secret' | 'grant'

/** One row the kit draws. Deliberately a display subset of whatever the
 *  variant's domain object is: the variant maps candidates/entries to rows
 *  before showing them, so the kit never depends back on the app. */
export interface FloatingPanelRow {
  /** Render the display column yourself.
   *
   *  The kit cannot know that a row holds a shell command, and it must not
   *  learn: ui/ may not import from outside itself, or the kit starts
   *  depending on the surfaces that depend on it. So a surface whose rows
   *  need more than text-plus-marks — recall, whose rows are commands and
   *  whose vault references read as chips — passes the renderer in. The
   *  kit hands it the mark class so the highlight still belongs to the
   *  panel. Absent: the plain text-plus-marks path below. */
  renderText?: (
    text: string,
    matchRanges: ReadonlyArray<{ from: number; to: number }>,
    markClass: string,
  ) => DocumentFragment
  readonly id: string
  /** What a row shows — never what a pick inserts (the controller owns
   *  insertText and the replacement range). */
  readonly displayText: string
  /** The substrings of displayText to highlight as the match. */
  readonly matchRanges: ReadonlyArray<{ from: number; to: number }>
  /** Right-aligned evidence column (kind/source badges, relative
   *  timestamps) — built by the variant, placed by the primitive. */
  readonly actions?: ReadonlyArray<Node>
  /** A section caption above this row when the previous row is in a
   *  different section (the history group in a mixed completion list). The
   *  variant DECIDES sections; the primitive renders the caption. */
  readonly group?: string
}

export interface FloatingPanelCallbacks {
  /** A row was hovered (mouse) — the variant moves its selection. */
  onHover?(index: number): void
  /** A row was clicked — the variant accepts it. */
  onPick?(index: number): void
}

export interface FloatingPanelShow {
  readonly rows: ReadonlyArray<FloatingPanelRow>
  /** The variant's decision; the view only draws it. */
  readonly selectedIndex: number
  /** The caret's x, in px relative to the panel's offset parent (the editor
   *  root) — the panel opens at the caret, not at the pane's edge; null
   *  keeps the kit's left-edge default. */
  readonly anchorLeft?: number | null
  /** Variant chrome placed before the list (the recall's header). */
  readonly before?: ReadonlyArray<HTMLElement>
  /** Variant chrome placed between the list and the footer (the recall's
   *  detail pane and search row). */
  readonly after?: ReadonlyArray<HTMLElement>
  /** The key-hint groups for the footer; absent renders no footer (the
   *  completion's empty row must not promise keys that do nothing). */
  readonly footer?: ReadonlyArray<string>
}

export class FloatingPanel {
  readonly root: HTMLElement
  private callbacks: FloatingPanelCallbacks
  private _open = false
  private listEl: HTMLElement | null = null
  /** The measured panel width for the CURRENT list content, plus the
   *  content signature it was measured against. The width is stable for the
   *  life of one open list (the owner's "every Tab press makes the window
   *  narrower"): measured once per rendered list, never re-measured on a
   *  selection change — a narrower selected row must not shrink the panel
   *  under the cursor. hide() clears both, so the next list measures fresh. */
  private measuredWidth: number | null = null
  private measuredSignature: string | null = null
  /** The content signature of the rows currently rendered. The selection
   *  index is deliberately not part of it. */
  private rowsSignature = ''
  /** Which surface this is — the width floor is read from it. */
  private readonly variant: FloatingPanelVariant

  constructor(opts: {
    variant: FloatingPanelVariant
    role: string
    ariaLabel: string
    callbacks?: FloatingPanelCallbacks
  }) {
    this.callbacks = opts.callbacks ?? {}
    this.variant = opts.variant
    this.root = document.createElement('div')
    this.root.className = 'ui-floating-panel'
    this.root.dataset.variant = opts.variant
    this.root.setAttribute('role', opts.role)
    this.root.setAttribute('aria-label', opts.ariaLabel)
    this.root.dataset.open = 'false'
  }

  get isOpen(): boolean {
    return this._open
  }

  /** The scrollable list of the open render — the variant reveals its
   *  selected row against it after show(). Null while closed. */
  get list(): HTMLElement | null {
    return this.listEl
  }

  /** Mount the panel as a child of the editor's root, so it floats above
   *  the editor (the root is position: relative). */
  mount(container: HTMLElement): void {
    container.appendChild(this.root)
  }

  /**
   * Render the current list. Selected index is the variant's decision (it
   * owns the state machine); the view only draws it. The width is measured
   * once per rendered list content and is stable for the life of one open
   * list.
   */
  show(opts: FloatingPanelShow): void {
    this._open = true
    this.root.dataset.open = 'true'
    this.root.replaceChildren()

    for (const el of opts.before ?? []) this.root.appendChild(el)

    const list = document.createElement('div')
    list.className = 'ui-floating-panel__list'
    let prevGroup: string | undefined
    const rows = opts.rows
    for (let i = 0; i < rows.length; i++) {
      const r = rows[i]
      // A section caption appears only where the section CHANGES: the
      // completion stamps the history rows' group only in a mixed list, so
      // a pure-history list (one section) never reads a caption.
      if (r.group !== undefined && r.group !== prevGroup) {
        const group = document.createElement('div')
        group.className = 'ui-floating-panel__group'
        group.setAttribute('role', 'presentation')
        group.textContent = r.group
        list.appendChild(group)
      }
      prevGroup = r.group
      list.appendChild(this.renderRow(r, i, i === opts.selectedIndex))
    }
    this.root.appendChild(list)
    this.listEl = list

    for (const el of opts.after ?? []) this.root.appendChild(el)

    if (opts.footer) this.root.appendChild(this.renderFooter(opts.footer))

    // The width cache key: the row content that decides the widest row. The
    // selection index is deliberately not part of it — cycling through the
    // same list must never re-measure (the width is stable for the life of
    // one open list).
    this.rowsSignature = rows
      .map((r) => `${r.id}\u0000${r.displayText}\u0000${r.group ?? ''}\u0000${this.actionsText(r)}`)
      .join('\u0001')
    this.applyGeometry(opts.anchorLeft)
  }

  /**
   * The honest "nothing to choose" state: one non-selectable row naming why
   * (zero candidates is a state the product shows, never silence). No
   * footer — the hints describe a selectable list, and this row has nothing
   * to insert, cycle or navigate.
   */
  showEmpty(message: string, anchorLeft?: number | null): void {
    this._open = true
    this.root.dataset.open = 'true'
    this.root.replaceChildren()

    const list = document.createElement('div')
    list.className = 'ui-floating-panel__list'
    const rowEl = document.createElement('div')
    rowEl.className = 'ui-collection-row ui-floating-panel__row'
    rowEl.dataset.empty = 'true'
    rowEl.setAttribute('role', 'option')
    rowEl.setAttribute('aria-selected', 'false')
    rowEl.setAttribute('aria-disabled', 'true')
    const info = document.createElement('div')
    info.className = 'ui-collection-row__info'
    info.textContent = message
    rowEl.appendChild(info)
    list.appendChild(rowEl)
    this.root.appendChild(list)
    this.listEl = list
    this.rowsSignature = message
    this.applyGeometry(anchorLeft)
  }

  /** Close the panel and drop its rows. The width cache dies with the
   *  list — the next open list measures fresh. */
  hide(): void {
    this._open = false
    this.root.dataset.open = 'false'
    this.root.replaceChildren()
    this.listEl = null
    this.measuredWidth = null
    this.measuredSignature = null
    this.rowsSignature = ''
  }

  destroy(): void {
    this.hide()
    this.root.remove()
  }

  /**
   * The panel is as wide as its widest content, capped — content-sized,
   * never the editor's width. The width is measured against the ROOT (the
   * footer and variant sections count, not only the rows), floored, and
   * cached per list signature. The left edge is the caret anchor, clamped
   * so the panel never runs off the editor's right edge.
   */
  private applyGeometry(anchorLeft: number | null | undefined): void {
    const cap = Math.min(MAX_PANEL_WIDTH_PX, window.innerWidth * 0.9)
    // One measurement per rendered list: the width is stable for the life
    // of one open list (the owner's "every Tab press makes the window
    // narrower"). A selection change re-renders the same rows and must not
    // re-measure; a list that CHANGES (a late batch merging in) re-measures.
    const floor = MIN_PANEL_WIDTH_PX[this.variant]
    if (this.measuredSignature !== this.rowsSignature) {
      this.measuredWidth = Math.max(this.root.scrollWidth, floor)
      this.measuredSignature = this.rowsSignature
    }
    this.root.style.width = `${Math.min(this.measuredWidth ?? floor, cap)}px`

    if (anchorLeft === null || anchorLeft === undefined) {
      this.root.style.left = ''
      return
    }
    const parent = this.root.parentElement
    const parentWidth = parent?.clientWidth ?? window.innerWidth
    const width = this.root.offsetWidth
    const left = Math.max(0, Math.min(anchorLeft, Math.max(0, parentWidth - width)))
    this.root.style.left = `${left}px`
  }

  /** One selectable row: the shared collection-row shell, the display text
   *  with the matched ranges as marks, and the variant's evidence column. */
  private renderRow(r: FloatingPanelRow, index: number, selected: boolean): HTMLElement {
    const rowEl = document.createElement('div')
    rowEl.className = 'ui-collection-row ui-floating-panel__row'
    rowEl.setAttribute('role', 'option')
    rowEl.setAttribute('aria-selected', String(selected))
    if (selected) rowEl.dataset.selected = 'true'

    const info = document.createElement('div')
    info.className = 'ui-collection-row__info'
    info.appendChild(this.renderDisplay(r))
    rowEl.appendChild(info)

    if (r.actions && r.actions.length > 0) {
      const actions = document.createElement('div')
      actions.className = 'ui-collection-row__actions'
      for (const node of r.actions) actions.appendChild(node)
      rowEl.appendChild(actions)
    }

    if (this.callbacks.onHover || this.callbacks.onPick) {
      rowEl.addEventListener('mouseenter', () => this.callbacks.onHover?.(index))
      rowEl.addEventListener('mousedown', (e) => {
        e.preventDefault()
        this.callbacks.onPick?.(index)
      })
    }
    return rowEl
  }

  /** The display column: displayText with the matched ranges as marks, and
   *  — when the row holds a COMMAND — its vault references as chips. The
   *  command rendering is shared with every other surface that shows one
   *  (command-text.ts), so a reference does not have to be taught to each
   *  window separately. */
  private renderDisplay(r: FloatingPanelRow): DocumentFragment {
    if (r.renderText) {
      return r.renderText(r.displayText, r.matchRanges, MATCH_CLASS)
    }
    const frag = document.createDocumentFragment()
    let pos = 0
    const ranges = [...r.matchRanges].sort((a, b) => a.from - b.from)
    for (const range of ranges) {
      const from = Math.max(pos, Math.min(range.from, r.displayText.length))
      const to = Math.max(from, Math.min(range.to, r.displayText.length))
      if (from > pos) frag.appendChild(document.createTextNode(r.displayText.slice(pos, from)))
      if (to > from) {
        const mark = document.createElement('mark')
        mark.className = MATCH_CLASS
        mark.textContent = r.displayText.slice(from, to)
        frag.appendChild(mark)
      }
      pos = to
    }
    if (pos < r.displayText.length) {
      frag.appendChild(document.createTextNode(r.displayText.slice(pos)))
    }
    return frag
  }

  /** One line, every hint in it: the groups are spans laid out by flex. */
  private renderFooter(hints: ReadonlyArray<string>): HTMLElement {
    const footer = document.createElement('div')
    footer.className = 'ui-floating-panel__footer'
    for (const hint of hints) {
      const span = document.createElement('span')
      span.textContent = hint
      footer.appendChild(span)
    }
    return footer
  }

  /** The actions' text, for the width cache key (a badge can widen a row). */
  private actionsText(r: FloatingPanelRow): string {
    let text = ''
    for (const node of r.actions ?? []) text += node.textContent ?? ''
    return text
  }
}
