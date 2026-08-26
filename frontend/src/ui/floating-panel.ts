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

/** The gap between the panel and the field it is anchored to, in px — the
 *  viewport-anchored twin of the `margin-bottom` the editor-mounted panel
 *  gets from CSS. It lives here because the anchored path computes `top`
 *  arithmetically and CSS margins cannot take part in that arithmetic. */
const ANCHOR_GAP_PX = 6

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
  /** The panel was dismissed by one of the optional document gestures. */
  onDismiss?(reason: 'escape' | 'outside'): void
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
  /** The element that opens/owns this panel, kept inside the dismissal boundary. */
  private readonly dismissBoundary: HTMLElement | undefined
  /** The field this panel opens against, when it is NOT mounted inside that
   *  field's positioned ancestry — see the constructor. Undefined is the
   *  editor-mounted panel, whose placement is entirely CSS's. */
  private readonly anchor: (() => HTMLElement | null) | undefined
  /** What mount() was given. The anchored panel re-homes itself into the
   *  anchor's LAYER at every open (see enterLayer), and this is where it goes
   *  back to when the anchor is in the ordinary document. */
  private mountContainer: HTMLElement | null = null
  /** The anchorLeft of the current open render, replayed when the viewport
   *  moves under an anchored panel (scroll, resize). */
  private lastAnchorLeft: number | null | undefined
  private viewportListenersAttached = false
  private readonly onViewportChange = (): void => {
    if (!this._open) return
    this.applyGeometry(this.lastAnchorLeft)
  }
  private dismissListenersAttached = false
  private readonly onDocumentKeydown = (event: KeyboardEvent): void => {
    if (!this._open || event.key !== 'Escape') return
    event.preventDefault()
    event.stopPropagation()
    this.callbacks.onDismiss?.('escape')
  }
  private readonly onDocumentPointerdown = (event: PointerEvent): void => {
    if (!this._open) return
    const target = event.target
    if (
      target instanceof Node &&
      (this.root.contains(target) || this.dismissBoundary?.contains(target) === true)
    )
      return
    this.callbacks.onDismiss?.('outside')
  }

  constructor(opts: {
    variant: FloatingPanelVariant
    role: string
    ariaLabel: string
    callbacks?: FloatingPanelCallbacks
    dismissBoundary?: HTMLElement
    /** THE SECOND WAY THIS PANEL IS PLACED, and a typed variance of the kit
     *  rather than a second panel (`data-anchor="viewport"`).
     *
     *  Absent — the editor-mounted panel. It is a child of a `position:
     *  relative` root (the command editor's), so the CSS `bottom: 100%`
     *  means "directly above the prompt" and nothing here computes a top.
     *  That is the terminal's placement and it is unchanged.
     *
     *  Present — a panel mounted OUTSIDE the field it belongs to, because a
     *  plain input has no positioned root to hang inside and a form row
     *  clips. The containing block is then the viewport, where `bottom:
     *  100%` puts the panel's bottom edge at y=0 and the whole panel above
     *  the top of the window: every field-mounted panel had always opened
     *  out of sight (nocx-vzdna). So the anchored variance switches to
     *  `position: fixed` and computes `top` from the FIELD's viewport rect,
     *  the way `anchorLeft` already computes the horizontal axis.
     *
     *  A callback rather than an element because a field's rect is only
     *  valid at the moment the panel opens: rows move, panes scroll, and
     *  the same adapter serves a field that is re-rendered under it.
     *
     *  THE REJECTED ALTERNATIVE: wrap each field in a `position: relative`
     *  element and mount the panel inside it, so `bottom: 100%` means what
     *  it means in the terminal. That keeps one placement rule, and it
     *  fails on the two things this panel has to survive. A form row is
     *  inside scrolling panes and a `<dialog>` with `overflow: hidden`, so
     *  an absolutely-positioned panel taller than the row is clipped by an
     *  ancestor rather than floating over it; and it would put positioning
     *  responsibility into every surface that hosts a field, which is the
     *  second vocabulary the kit exists to prevent. Fixed placement escapes
     *  both, at the price of the arithmetic below. */
    anchor?: () => HTMLElement | null
  }) {
    this.callbacks = opts.callbacks ?? {}
    this.variant = opts.variant
    this.dismissBoundary = opts.dismissBoundary
    this.anchor = opts.anchor
    this.root = document.createElement('div')
    this.root.className = 'ui-floating-panel'
    this.root.dataset.variant = opts.variant
    if (opts.anchor !== undefined) this.root.dataset.anchor = 'viewport'
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
   *  the editor (the root is position: relative). An ANCHORED panel is
   *  mounted outside its field instead — here or on the body — and re-homes
   *  itself into the anchor's layer at every open (enterLayer). */
  mount(container: HTMLElement): void {
    this.mountContainer = container
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
    this.attachDismissListeners()
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
    this.enterLayer()
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
    this.attachDismissListeners()
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
    this.enterLayer()
    this.applyGeometry(anchorLeft)
  }

  /** Close the panel and drop its rows. The width cache dies with the
   *  list — the next open list measures fresh. */
  hide(): void {
    this.detachDismissListeners()
    this.detachViewportListeners()
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
    this.lastAnchorLeft = anchorLeft

    if (this.anchor !== undefined) {
      this.applyAnchoredPosition(anchorLeft)
      return
    }

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

  /**
   * The anchored panel's BOTH axes, against the viewport.
   *
   * The horizontal rule is the one the editor-mounted panel already has,
   * read against the window instead of the offset parent: open at the
   * anchor, never past the right edge. The vertical rule is the one CSS was
   * expressing with `bottom: 100%` and could not express here: above the
   * field when the panel fits above it, below when it does not, and clamped
   * into the window either way — a field near the top of the page and a
   * field near the bottom both open a panel a person can see.
   *
   * The clamp is what makes "inside the viewport" true rather than likely:
   * a panel taller than the space on both sides of the field still lands
   * with `top >= 0` and its bottom edge inside the window, and its own
   * `max-height: 40vh` keeps that reachable.
   */
  private applyAnchoredPosition(anchorLeft: number | null | undefined): void {
    const host = this.anchor?.() ?? null
    const rect = host?.getBoundingClientRect() ?? null
    const height = this.root.offsetHeight
    const width = this.root.offsetWidth
    const maxTop = Math.max(0, window.innerHeight - height)
    const maxLeft = Math.max(0, window.innerWidth - width)

    if (rect === null) {
      // No anchor to read — a field removed from the document under an open
      // panel. Somewhere visible beats the initial containing block's
      // `bottom: 100%`, which is off the top of the window.
      this.root.style.top = '0px'
      this.root.style.left = '0px'
      return
    }

    const above = rect.top - ANCHOR_GAP_PX - height
    const below = rect.bottom + ANCHOR_GAP_PX
    const top = above >= 0 ? above : below
    this.root.style.top = `${Math.max(0, Math.min(top, maxTop))}px`

    // anchorLeft stays what it is everywhere else — an offset from the
    // anchor's own left edge (the caret, where a host knows it) — so the
    // two callers do not need two meanings for one field.
    const desired = rect.left + (anchorLeft ?? 0)
    this.root.style.left = `${Math.max(0, Math.min(desired, maxLeft))}px`
  }

  /** The LAYER an anchored panel opens in, and it is not always the one it
   *  was mounted into. A native modal `<dialog>` paints in the browser's top
   *  layer, which is outside every stacking context: a panel left on the
   *  body would be behind the dialog's own scrim, visible in a screenshot
   *  and unclickable — the same defect as being off-screen, wearing a
   *  different coat. So an anchored panel moves to the open dialog its field
   *  lives in, and back to its mount container when the field is in the
   *  ordinary document (the endpoints page hosts the same field both ways).
   *  Fixed placement is unaffected by the move: it is the viewport either
   *  way, and an ancestor's `overflow: hidden` does not clip a fixed
   *  descendant. */
  private enterLayer(): void {
    if (this.anchor === undefined) return
    this.attachViewportListeners()
    const host = this.anchor()
    const dialog = host?.closest('dialog') ?? null
    const layer: HTMLElement | null = dialog !== null && dialog.open ? dialog : this.mountContainer
    if (layer !== null && this.root.parentElement !== layer) layer.appendChild(this.root)
  }

  /** An anchored panel is placed against a rect that the page can move under
   *  it. Capture-phase scroll catches the inner scrollers a form row sits
   *  in, not only the document's. */
  private attachViewportListeners(): void {
    if (this.viewportListenersAttached) return
    window.addEventListener('resize', this.onViewportChange)
    document.addEventListener('scroll', this.onViewportChange, true)
    this.viewportListenersAttached = true
  }

  private detachViewportListeners(): void {
    if (!this.viewportListenersAttached) return
    window.removeEventListener('resize', this.onViewportChange)
    document.removeEventListener('scroll', this.onViewportChange, true)
    this.viewportListenersAttached = false
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
  private attachDismissListeners(): void {
    if (!this.callbacks.onDismiss || this.dismissListenersAttached) return
    document.addEventListener('keydown', this.onDocumentKeydown, true)
    document.addEventListener('pointerdown', this.onDocumentPointerdown, true)
    this.dismissListenersAttached = true
  }

  private detachDismissListeners(): void {
    if (!this.dismissListenersAttached) return
    document.removeEventListener('keydown', this.onDocumentKeydown, true)
    document.removeEventListener('pointerdown', this.onDocumentPointerdown, true)
    this.dismissListenersAttached = false
  }

  /** The actions' text, for the width cache key (a badge can widen a row). */
  private actionsText(r: FloatingPanelRow): string {
    let text = ''
    for (const node of r.actions ?? []) text += node.textContent ?? ''
    return text
  }
}
