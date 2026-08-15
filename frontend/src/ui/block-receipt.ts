// BlockReceipt — the kit's after-submit capture receipt (ui/README table).
// Attached to the FROZEN BLOCK of the command that carried a credential,
// never floated over the prompt: the offer stopped interrupting composition,
// and this is where it arrives — after Enter, on the block that just
// finished, the way Bitwarden offers to save a password after the form is
// submitted.
//
// The contract (the round's brief):
//
//   - Focus stays in the editor when the receipt appears. It is not modal
//     and it does not steal a keystroke. ⇧⌘S moves focus INTO the receipt
//     (review mode — the only place in this design where focus leaves the
//     editor); Escape returns it.
//   - One row per capture: the kind's human label, the masked value built
//     from the redaction's prefix/suffix, and an editable name pre-filled
//     with the backend's suggestion.
//   - One primary action whose label names its scope: Save for one row,
//     Save 2 for two. It saves every row still in play. A row can be
//     dropped individually.
//   - Hovering a row emphasises the corresponding chip in the block's
//     command line (the host wires the highlight; the receipt only reports
//     which row is under the pointer).
//   - The receipt does not expire. It lives on its block until the offer is
//     answered — Save or Dismiss — or until one of the real destruction
//     events takes the capture with it (the tab closing, the vault sealing,
//     the transport dropping, the app quitting). Several blocks can carry an
//     unanswered receipt at once; deciding about a key is rarely the next
//     thing anyone does.
//
// All controls are the kit's identities: ui-button, ui-text-field__input,
// ui-badge. The receipt places them and never repaints them — its own CSS
// file owns only the ui-block-receipt family.
export interface BlockReceiptCapture {
  readonly captureId: string
  /** The kind's human label (KIND_LABELS). */
  readonly kindLabel: string
  /** The masked value from the redaction's prefix/suffix — never
   *  re-derived from anything the renderer holds. */
  readonly maskedValue: string
  /** The backend's suggested vault name, pre-filled. */
  readonly suggestedName: string
}

export interface BlockReceiptCallbacks {
  /** The primary action (the Save button or ⌘S): save every row still in
   *  play, with the names as the inputs currently hold them. */
  onSaveAll(rows: ReadonlyArray<{ captureId: string; name: string }>): void
  /** A row's drop control: dismiss that capture. */
  onDismiss(captureId: string): void
  /** Hover moved between rows: emphasise that row's chip in the block
   *  (null = no row under the pointer). */
  onHover(captureId: string | null): void
  /** Escape from inside the receipt: return focus to the editor. */
  onExitReview(): void
}

export class BlockReceipt {
  readonly root: HTMLElement
  private readonly callbacks: BlockReceiptCallbacks
  private readonly rows: Map<string, { rowEl: HTMLElement; input: HTMLInputElement | null }> =
    new Map()
  private readonly primaryBtn: HTMLButtonElement
  private readonly actionsEl: HTMLElement

  constructor(captures: ReadonlyArray<BlockReceiptCapture>, callbacks: BlockReceiptCallbacks) {
    this.callbacks = callbacks
    this.root = document.createElement('div')
    this.root.className = 'ui-block-receipt'
    this.root.setAttribute('role', 'group')
    this.root.setAttribute('aria-label', 'save detected secret')

    const rowEls: HTMLElement[] = []
    for (const capture of captures) {
      const rowEl = this.buildRow(capture)
      rowEls.push(rowEl)
      this.root.appendChild(rowEl)
    }

    // ── The one primary action, labelled with its scope ────────────────
    this.actionsEl = document.createElement('div')
    this.actionsEl.className = 'ui-block-receipt__actions'
    this.primaryBtn = document.createElement('button')
    this.primaryBtn.className = 'ui-button ui-block-receipt__primary'
    this.primaryBtn.dataset.variant = 'primary'
    this.primaryBtn.addEventListener('click', () => this.saveAll())

    // With one capture the two buttons belong on the same line — Save is
    // the whole receipt's action AND that row's action, and stacking them
    // in a bar of their own left one button orphaned under another with
    // room to spare beside it. With several, Save names its scope
    // ("Save 2") and cannot sit inside any single row, so it keeps the bar.
    if (rowEls.length === 1) {
      rowEls[0].appendChild(this.primaryBtn)
    } else {
      this.actionsEl.appendChild(this.primaryBtn)
      this.root.appendChild(this.actionsEl)
    }
    this.updatePrimaryLabel()

    // Escape returns focus to the editor; ⌘S performs the primary action.
    // Only reachable while focus is INSIDE the receipt — the editor's own
    // Escape/⌘S handling lives on the editor root, which this is not a
    // child of.
    this.root.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        this.callbacks.onExitReview()
        return
      }
      if ((e.key === 's' || e.key === 'S') && !e.altKey && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        e.stopPropagation()
        this.saveAll()
      }
    })
  }

  mount(container: HTMLElement): void {
    container.appendChild(this.root)
  }

  /** The primary action: collect the current names and hand them over. */
  saveAll(): void {
    const rows: Array<{ captureId: string; name: string }> = []
    for (const [captureId, row] of this.rows) {
      rows.push({ captureId, name: row.input?.value.trim() ?? '' })
    }
    if (rows.length > 0) this.callbacks.onSaveAll(rows)
  }

  /** A row saved cleanly: remove it. The primary label recomputes; when
   *  the last row goes, so does the receipt (the toast already said so).
   *  Returns true once the receipt is empty, so the host can forget it —
   *  otherwise a destroyed receipt stays in the host's map and keeps being
   *  offered the save chord. */
  removeRow(captureId: string): boolean {
    const row = this.rows.get(captureId)
    if (!row) return this.rows.size === 0
    row.rowEl.remove()
    this.rows.delete(captureId)
    if (this.rows.size === 0) {
      this.destroy()
      return true
    }
    this.updatePrimaryLabel()
    return false
  }

  /** A row's save failed (or is partial): show the message on the row and
   *  keep it — the same capture can be retried. */
  markFailed(captureId: string, message: string): void {
    const row = this.rows.get(captureId)
    if (!row) return
    let err = row.rowEl.querySelector<HTMLElement>('.ui-block-receipt__row-error')
    if (!err) {
      err = document.createElement('div')
      err.className = 'ui-block-receipt__row-error'
      row.rowEl.appendChild(err)
    }
    err.textContent = message
  }

  /** ⇧⌘S: review mode — focus the first row's name field. */
  enterReview(): void {
    const first = this.rows.values().next().value as { input: HTMLInputElement | null } | undefined
    first?.input?.focus()
  }

  destroy(): void {
    this.root.remove()
  }
  private buildRow(capture: BlockReceiptCapture): HTMLElement {
    const rowEl = document.createElement('div')
    rowEl.className = 'ui-block-receipt__row'
    rowEl.dataset.captureId = capture.captureId

    const badge = document.createElement('span')
    badge.className = 'ui-badge ui-block-receipt__kind'
    badge.dataset.tone = 'warning'
    badge.textContent = capture.kindLabel

    const value = document.createElement('code')
    value.className = 'ui-block-receipt__value'
    value.textContent = capture.maskedValue

    let input: HTMLInputElement | null = null
    input = document.createElement('input')
    input.className = 'ui-text-field__input'
    input.type = 'text'
    input.value = capture.suggestedName
    input.setAttribute('aria-label', `vault name for ${capture.kindLabel}`)
    input.autocomplete = 'off'
    input.spellcheck = false

    const drop = document.createElement('button')
    drop.className = 'ui-button ui-block-receipt__drop'
    drop.dataset.variant = 'ghost'
    // Not "Remove": nothing has been stored yet, so there is nothing to
    // remove, and a destructive word over an offer reads as though
    // declining will delete something.
    drop.textContent = 'Dismiss'
    drop.setAttribute('aria-label', `do not save this ${capture.kindLabel}`)
    drop.addEventListener('click', () => this.callbacks.onDismiss(capture.captureId))

    const children: Array<Node | string> = [badge, value]
    if (input) children.push(input)
    children.push(drop)
    rowEl.append(...children)

    // Hover emphasises this row's chip in the block's command line — and
    // only this one.
    rowEl.addEventListener('mouseenter', () => this.callbacks.onHover(capture.captureId))
    rowEl.addEventListener('mouseleave', () => this.callbacks.onHover(null))

    this.rows.set(capture.captureId, { rowEl, input })
    return rowEl
  }

  private updatePrimaryLabel(): void {
    const n = this.rows.size
    this.primaryBtn.textContent = n === 1 ? 'Save' : `Save ${n}`
  }

  // ── Connection-offer variant (nocx-pu4.7) ───────────────────────────

  /**
   * Build a receipt that offers to save an SSH destination as a managed
   * connection. Same behaviour contract as the vault-capture receipt —
   * block-attached, non-modal, no focus steal, one primary action, no
   * expiry — with connection-specific labels. The kit grows by VARIANT
   * (ui/README.md), not by near-duplicate.
   */
  static forConnection(
    destination: string,
    suggestedName: string,
    callbacks: {
      onSave(name: string): void
      onDismiss(): void
    },
  ): BlockReceipt {
    const capture: BlockReceiptCapture = {
      captureId: `conn-offer:${destination}`,
      kindLabel: 'SSH host',
      maskedValue: destination,
      suggestedName,
    }
    return new BlockReceipt([capture], {
      onSaveAll: (rows) => callbacks.onSave(rows[0].name),
      onDismiss: () => callbacks.onDismiss(),
      onHover: () => {},
      onExitReview: () => {},
    })
  }
}
