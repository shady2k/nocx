// BlockNotice — the kit's one line a block states about ITSELF after the
// fact, with the actions that act on what it says (nocx-2019q).
//
// The case it exists for: something became true because of what a person just
// did — a standing answer was saved, a decision was recorded — and the place
// that fact belongs is the block where they did it, not a page they have no
// reason to open and not a toast that is gone before they look up. It is a
// STATEMENT, not an offer: nothing is pending, and the actions beside it act
// on what has already happened (take it back, go and manage it).
//
// WHY NOT BlockReceipt, which is also block-attached and also vanilla. That
// component is an OFFER — "shall I save this?" — and its whole shape says so:
// a row per candidate, an editable name for each, one primary Save whose
// label counts its scope, and a per-row Dismiss meaning "do not save this
// one". Nothing in it can carry "this is already saved; here is how to undo
// it" without one of its parts meaning the opposite of its name — the drop
// control would have to become a navigation, and the primary a destructive
// undo. Two different concepts, and the kit grows by variants only where the
// concept is the same one (ui/README.md).
//
// A surface PLACES this and never repaints it. The actions are the kit's own
// ui-button identity; this module's CSS file owns the ui-block-notice family
// and nothing else.

/** One thing that can be done about what the line says. */
export interface BlockNoticeAction {
  readonly label: string
  /** What screen readers hear, when the label alone is not the whole act. */
  readonly ariaLabel?: string
  readonly onActivate: () => void
  /** The kit Button variant. Default `ghost`: these sit inside a transcript
   *  and must not compete with the block's own controls for the eye. */
  readonly variant?: 'ghost' | 'default' | 'primary' | 'danger'
}

export interface BlockNoticeState {
  /** The whole of what happened, in one sentence. */
  readonly text: string
  /** `saved` for a fact that came out right, `warning` for a degrade the
   *  product must show rather than log. */
  readonly tone?: 'saved' | 'warning'
  readonly actions?: ReadonlyArray<BlockNoticeAction>
}

export class BlockNotice {
  readonly root: HTMLElement
  private readonly textEl: HTMLElement
  private readonly actionsEl: HTMLElement

  constructor(state: BlockNoticeState) {
    this.root = document.createElement('div')
    this.root.className = 'ui-block-notice'
    // A statement about what just happened, announced once when it arrives
    // and again when an action changes it — polite, because it is never
    // urgent enough to interrupt what is being read.
    this.root.setAttribute('role', 'status')
    this.root.setAttribute('aria-live', 'polite')

    this.textEl = document.createElement('span')
    this.textEl.className = 'ui-block-notice__text'
    this.actionsEl = document.createElement('div')
    this.actionsEl.className = 'ui-block-notice__actions'
    this.root.append(this.textEl, this.actionsEl)

    this.say(state)
  }

  mount(container: HTMLElement): void {
    container.appendChild(this.root)
  }

  /**
   * Restate the line. An action that CHANGED what is true says so in place —
   * one line, one truth. Appending a second sentence beside the first would
   * leave the superseded one on screen, and a person reading the transcript
   * later cannot tell which of two statements is the current one.
   */
  say(state: BlockNoticeState): void {
    this.root.dataset.tone = state.tone ?? 'saved'
    this.textEl.textContent = state.text
    this.actionsEl.replaceChildren()
    for (const action of state.actions ?? []) {
      this.actionsEl.appendChild(buildAction(action))
    }
  }

  destroy(): void {
    this.root.remove()
  }
}

function buildAction(action: BlockNoticeAction): HTMLButtonElement {
  const button = document.createElement('button')
  button.className = 'ui-button'
  button.type = 'button'
  button.dataset.variant = action.variant ?? 'ghost'
  button.textContent = action.label
  if (action.ariaLabel !== undefined) button.setAttribute('aria-label', action.ariaLabel)
  button.addEventListener('click', () => action.onActivate())
  return button
}
