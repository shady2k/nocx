// Passive DOM command editor (ADR-0004 §3). Holds text + selection only; a
// registered action decides where a submit goes. Keyboard routing to/from the
// PTY is by FOCUS: while shown the textarea captures keys; while hidden the
// xterm has focus and keys flow to the PTY as usual.
export interface EditorActions {
  submit: (doc: string) => void
}

export class CommandEditor {
  private root: HTMLElement
  private ta: HTMLTextAreaElement

  constructor(private readonly actions: EditorActions) {
    this.root = document.createElement('div')
    this.root.className = 'nocx-editor'
    this.root.style.display = 'none'
    this.ta = document.createElement('textarea')
    this.ta.className = 'nocx-editor-input'
    this.ta.rows = 1
    this.ta.spellcheck = false
    this.ta.autocapitalize = 'off'
    this.ta.addEventListener('keydown', this.onKeydown)
    this.root.appendChild(this.ta)
  }

  mount(container: HTMLElement): void {
    container.appendChild(this.root)
  }

  private onKeydown = (e: KeyboardEvent): void => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      const doc = this.ta.value
      // Atomic handoff (ADR-0004 §2): hide + clear BEFORE sending, so the
      // committed command is painted once by the shell, not echoed twice.
      this.ta.value = ''
      this.hide()
      this.actions.submit(doc)
    }
  }

  show(): void {
    this.root.style.display = ''
    this.ta.focus()
  }

  hide(): void {
    this.ta.blur()
    this.root.style.display = 'none'
  }

  get isVisible(): boolean {
    return this.root.style.display !== 'none'
  }

  dispose(): void {
    this.ta.removeEventListener('keydown', this.onKeydown)
    this.root.remove()
  }
}
