// SecretPicker — the reference picker, a variant of the kit's FloatingPanel
// (`ui-floating-panel[data-variant='secret']`). '@' at a word start opens
// it; picking inserts `{{secret:NAME}}` over the trigger word.
//
// The picker is PASSIVE, and that deliberately contradicts the completion
// dropdown's rules (the owner's decision):
//
//   - '@' inserts an ordinary '@' into the document and enters NO mode.
//     Keystrokes keep going to the line; this panel only filters, driven by
//     the controller watching the document (setFilter).
//   - Nothing matches -> the panel CLOSES, silently. That rule exists for
//     Tab because Tab is an explicit request and silence in answer to a
//     request is a defect; '@' is a passive trigger, and a panel that stays
//     up on no-match becomes a modal mode the user has to escape. Do NOT
//     "unify" the two — the contrast is the contract.
//   - A space closes it (the controller sees the whitespace in the trigger
//     word and closes). Esc closes it and leaves the '@' as text. Only
//     Enter or Tab on a selected row inserts the reference.
//
// Same shape as an @-mention in a chat app: it offers, it never traps.
//
// The vault's lifecycle states are OFFERS, never error rows (the owner's
// decision): sealed offers to unseal, uninitialized offers to set up — both
// from inside the panel, without leaving the prompt. Empty (unsealed, no
// secrets) is the honest empty state.
import { FloatingPanel, type FloatingPanelRow } from './floating-panel'

// The kit must not depend back on the app (no-restricted-imports), so the
// source is declared with the minimal shapes the panel reads: the vault's
// lifecycle state and the inventory's name/id. The composition root
// satisfies these structurally.
export interface SecretPickerSource {
  /** The vault's lifecycle state — decides list vs. offer rows. */
  status(): Promise<{ state: 'uninitialized' | 'sealed' | 'unsealed' }>
  /** Every secret the vault holds, by name (ADR-0016). */
  list(): Promise<SecretEntry[]>
  /** The user activated the sealed offer row: the dispatcher seam raises the
   *  unlock prompt and retries; resolves when the vault is open, rejects on
   *  cancel/refusal. */
  requestUnseal(): Promise<void>
  /** The user activated the uninitialized offer row: silent setup when the
   *  OS key is capable; otherwise the host raises the setup dialog. Resolves
   *  true when a dialog took over the surface (the panel closes — the
   *  dialog is the surface now), false when setup happened silently and the
   *  list can reload. */
  requestSetup(): Promise<boolean>
  /** The user activated "Add a secret…". Form hosts return the newly
   *  created row so the field can insert its opaque handle in place; hosts
   *  without a value-entry form (the terminal prompt) return `undefined`
   *  after handing the person to their existing destination. The hosts differ
   *  because a form has somewhere to type a value while a floating row over a
   *  prompt does not. */
  requestCreate(name: string): Promise<SecretEntry | undefined>
  /** Host logging seam for failures reading lifecycle or inventory state. */
  onError?: (message: string, error: unknown) => void
}

export interface SecretPickerCallbacks {
  /** Insert `{{secret:NAME}}` over the trigger word; the host owns the
   *  editor seam and the replacement range. */
  onInsert(name: string): void
  /** Report a refused picker read through the host's logger. */
  onError?(message: string, error: unknown): void
}

/** The one fact about a secret row the panel reads: its name (the vault's
 *  inventory name, ADR-0016) and the row handle the composition root
 *  addresses it by. */
export interface SecretEntry {
  id: string
  name: string
}

/** The group caption every row carries — the one group today, and the
 *  mechanism a future reference kind joins as (the owner's decision: ONE
 *  list, grouped; a submenu is a mouse in a keyboard-first tool). */
const GROUP_LABEL = 'Secrets'

// The words the vault's states are OFFERED in. Exported because a second
// surface reaches for the vault (the tab-strip picker), and two surfaces
// that describe one vault in their own words drift into disagreeing about
// it. The offers themselves are each surface's to render — these are the
// sentences.
export const VAULT_OFFER_UNSEAL = 'Unlock the vault to use its secrets'
export const VAULT_OFFER_SETUP = 'Set up the vault to store secrets'

/** The create offer, carrying what was typed — almost always the name the
 *  person was reaching for, and asking them to type it again is how a
 *  feature goes unused. */
export function addSecretLabel(typed: string): string {
  return typed === '' ? 'Add a secret…' : `Add "${typed}" to the vault…`
}

/** The synthetic last row of the list state: "Add a secret…". Not a vault
 *  entry, so it is addressed by a reserved id rather than by index alone. */
const CREATE_ROW_ID = '\u0000create'

/** Why the picker is open. 'insert' is the '@' trigger — the person is
 *  composing and wants to reach for a secret. 'resolve' is a recalled
 *  command whose credential the store removed: the panel's job there is
 *  first to say what is missing, and only then to offer the vault. */
export type SecretPickerPurpose = 'insert' | 'resolve'

type PickerState =
  | { readonly name: 'closed' }
  | { readonly name: 'loading' }
  | { readonly name: 'sealed' }
  | { readonly name: 'uninitialized' }
  | { readonly name: 'refused'; readonly message: string }
  | { readonly name: 'empty' }
  | {
      readonly name: 'list'
      readonly entries: SecretEntry[]
      readonly filter: string
      readonly selected: number
    }

export class SecretPicker {
  private state: PickerState = { name: 'closed' }
  private purpose: SecretPickerPurpose = 'insert'
  /** The controller's filter that arrived while the inventory was still in
   *  flight: typing `@ope` before the list lands must not render every
   *  secret unfiltered. Applied when the list renders; null when none. */
  private pendingFilter: string | null = null
  /** Invalidates a pending create result when the picker opens again. */
  private createRequest = 0
  private readonly panel: FloatingPanel

  constructor(
    private readonly source: SecretPickerSource,
    private readonly callbacks: SecretPickerCallbacks,
  ) {
    this.panel = new FloatingPanel({
      variant: 'secret',
      role: 'listbox',
      ariaLabel: 'vault secrets',
      callbacks: {
        onHover: (index) => this.hover(index),
        onPick: (index) => this.pick(index),
      },
    })
  }

  get root(): HTMLElement {
    return this.panel.root
  }

  get isOpen(): boolean {
    return this.state.name !== 'closed'
  }

  mount(container: HTMLElement): void {
    this.panel.mount(container)
  }

  /** Open the picker at the caret: fetch the vault's lifecycle, then the
   *  list or an offer row. The trigger position is the controller's; this
   *  surface only renders.
   *
   *  `purpose` decides what the panel SAYS when it has nothing to list.
   *  Opened from '@' the question is "which secret do you want here", and
   *  "set up the vault to store secrets" answers it. Opened to resolve a
   *  recalled command it is the wrong answer to a different question: the
   *  key was taken out of that command, and what the person needs to know
   *  first is that it is missing and that they can simply type it back. */
  async open(purpose: SecretPickerPurpose = 'insert'): Promise<void> {
    if (this.isOpen) return
    this.purpose = purpose
    this.createRequest++
    this.state = { name: 'loading' }
    this.render()
    try {
      const status = await this.source.status()
      if (status.state === 'sealed') {
        this.state = { name: 'sealed' }
        this.render()
        return
      }
      if (status.state === 'uninitialized') {
        this.state = { name: 'uninitialized' }
        this.render()
        return
      }
      await this.loadList('')
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      this.callbacks.onError?.(message, err)
      if (this.state.name === 'loading') {
        this.state = { name: 'refused', message }
        this.render()
      }
    }
  }

  /** The trigger word's continuation, pushed by the controller on every
   *  document change. Nothing matches -> close SILENTLY (see the header —
   *  the passive trigger's no-match is not the completion's no-match). */
  setFilter(filter: string): void {
    const s = this.state
    if (s.name === 'closed') return
    if (/\s/.test(filter)) {
      // A space ends the trigger word: close and leave the '@' as text.
      this.close()
      return
    }
    if (s.name === 'loading') {
      // The list is still in flight — carry the filter to the render below
      // instead of showing every secret unfiltered.
      this.pendingFilter = filter
      return
    }
    if (s.name !== 'list') return
    // A filter that matches nothing used to close the panel silently. That
    // was right when the panel could only offer what the vault already
    // held: nothing to show, so get out of the way. It stopped being right
    // when "Add a secret…" arrived — typing a name the vault does not have
    // is precisely the moment to offer making it, and closing at exactly
    // that keystroke takes the offer away as the user reaches for it. A
    // space still ends the trigger word (above): that is what "I am not
    // naming a secret any more" actually looks like.
    const rows = this.matches(s.entries, filter)
    const selected = Math.min(s.selected, rows.length)
    this.state = { ...s, filter, selected }
    this.render()
  }

  /** The keyboard arbiter's turn (after recall, before completion): the
   *  panel owns navigation and accept while open; EVERYTHING else — text,
   *  space, punctuation — falls through to the line, which is the passive
   *  contract. */
  handleKey(e: KeyboardEvent): boolean {
    if (e.isComposing || e.keyCode === 229) return false
    if (!this.isOpen) return false
    if (e.key === 'ArrowDown') {
      this.move(1)
      return this.consume(e)
    }
    if (e.key === 'ArrowUp') {
      this.move(-1)
      return this.consume(e)
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      this.activate()
      return this.consume(e)
    }
    if (e.key === 'Tab' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      // Tab takes the selected row without running it — the same meaning
      // recall gives Tab.
      this.activate()
      return this.consume(e)
    }
    if (e.key === 'Escape') {
      this.close()
      return this.consume(e)
    }
    // Space and every other key are the line's: the controller closes the
    // panel when the trigger word gains whitespace or stops matching.
    return false
  }

  close(): void {
    if (this.state.name === 'closed') return
    this.state = { name: 'closed' }
    this.pendingFilter = null
    this.panel.hide()
  }

  destroy(): void {
    this.close()
    this.panel.destroy()
  }

  // ── internals ────────────────────────────────────────────────────────────

  private matches(entries: SecretEntry[], filter: string): SecretEntry[] {
    const needle = filter.toLowerCase()
    return entries.filter((e) => e.name.toLowerCase().includes(needle))
  }

  private move(dir: -1 | 1): void {
    const s = this.state
    if (s.name !== 'list') return
    // One past the last entry is the "Add a secret…" row.
    const count = this.matches(s.entries, s.filter).length + 1
    const next = (s.selected + dir + count) % count
    this.state = { ...s, selected: next }
    this.render()
  }

  private hover(index: number): void {
    const s = this.state
    if (s.name !== 'list') return
    this.state = { ...s, selected: index }
    this.render()
  }

  /** Enter/Tab on a row: insert the reference (list), or act on the offer
   *  (sealed -> unseal, uninitialized -> set up). */
  private activate(): void {
    const s = this.state
    switch (s.name) {
      case 'list': {
        const rows = this.matches(s.entries, s.filter)
        // The create row sits one past the last entry.
        if (s.selected >= rows.length) {
          const typed = s.filter
          const request = ++this.createRequest
          this.close()
          void Promise.resolve(this.source.requestCreate(typed))
            .then((created) => {
              if (request !== this.createRequest || created === undefined) return
              this.callbacks.onInsert(created.name)
            })
            .catch(() => {})
          return
        }
        const entry = rows[s.selected]
        if (!entry) return
        this.close()
        this.callbacks.onInsert(entry.name)
        return
      }
      case 'sealed':
        void this.source
          .requestUnseal()
          .then(() => {
            if (this.state.name === 'sealed') void this.loadList('')
          })
          .catch(() => {
            // Cancelled or refused: the offer row stays; the panel says so.
          })
        return
      case 'uninitialized':
        void this.source
          .requestSetup()
          .then((dialogTookOver) => {
            if (dialogTookOver) {
              // The setup dialog owns the surface now; the panel must not
              // stay up behind it showing a stale offer row.
              this.close()
              return
            }
            if (this.state.name === 'uninitialized') void this.loadList('')
          })
          .catch(() => {})
        return
      default:
        return
    }
  }

  private async loadList(filter: string): Promise<void> {
    this.state = { name: 'loading' }
    this.render()
    try {
      const entries = await this.source.list()
      if (entries.length === 0) {
        // An empty vault is not a dead end when you are reaching for a
        // secret: the list state with no entries still renders the "Add a
        // secret…" row, which is the whole answer to "there is nothing
        // here". Resolving a recalled command is different — what is
        // missing there is the key itself, and the empty state says so.
        const initialFilter = this.pendingFilter ?? filter
        this.pendingFilter = null
        this.state =
          this.purpose === 'resolve'
            ? { name: 'empty' }
            : { name: 'list', entries: [], filter: initialFilter, selected: 0 }
        this.render()
        return
      }
      // A filter that arrived while loading wins over the open's initial
      // one — typing '@ope' before the inventory lands must not render
      // every secret. A no-match filter no longer closes the panel: see
      // setFilter for why.
      const initialFilter = this.pendingFilter ?? filter
      this.pendingFilter = null
      this.state = { name: 'list', entries, filter: initialFilter, selected: 0 }
      this.render()
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      this.callbacks.onError?.(message, err)
      if (this.state.name === 'loading') {
        const data = typeof err === 'object' && err !== null && 'data' in err ? err.data : null
        const sealed =
          typeof data === 'object' &&
          data !== null &&
          'reason' in data &&
          data.reason === 'vault-sealed'
        this.state = sealed ? { name: 'sealed' } : { name: 'refused', message }
        this.render()
      }
    }
  }

  private consume(e: KeyboardEvent): boolean {
    e.preventDefault()
    e.stopPropagation()
    return true
  }

  private render(): void {
    const s = this.state
    if (s.name === 'closed') return
    if (s.name === 'loading') {
      this.panel.showEmpty('loading secrets…')
      return
    }
    if (s.name === 'empty') {
      this.panel.showEmpty(
        this.purpose === 'resolve'
          ? 'the key was removed from this command — type it here, or save one to the vault to reuse it'
          : 'no secrets yet',
      )
      return
    }
    if (s.name === 'refused') {
      const row: FloatingPanelRow = {
        id: 'status-refused',
        displayText: `Could not read vault secrets: ${s.message}`,
        matchRanges: [],
        group: GROUP_LABEL,
        actions: [this.badge('unavailable')],
      }
      this.panel.show({
        rows: [row],
        selectedIndex: -1,
      })
      return
    }
    if (s.name === 'sealed') {
      const row: FloatingPanelRow = {
        id: 'offer-unseal',
        displayText:
          this.purpose === 'resolve'
            ? 'The key is missing here — unlock the vault to substitute it'
            : VAULT_OFFER_UNSEAL,
        matchRanges: [],
        group: GROUP_LABEL,
        actions: [this.badge('sealed')],
      }
      this.panel.show({
        rows: [row],
        selectedIndex: 0,
        footer: ['↵ to unlock', 'esc to dismiss'],
      })
      return
    }
    if (s.name === 'uninitialized') {
      const row: FloatingPanelRow = {
        id: 'offer-setup',
        // Resolving names the missing thing first. "Set up the vault to
        // store secrets" is a true sentence that answers a question nobody
        // asked here: the command will not run because its key is gone, and
        // the fastest way out is to type the key back in.
        displayText:
          this.purpose === 'resolve'
            ? 'The key is missing here — type it in, or set up the vault to keep it'
            : VAULT_OFFER_SETUP,
        matchRanges: [],
        group: GROUP_LABEL,
        actions: [this.badge('not set up')],
      }
      this.panel.show({
        rows: [row],
        selectedIndex: 0,
        footer: ['↵ to set up', 'esc to dismiss'],
      })
      return
    }
    const rows = this.matches(s.entries, s.filter)
    // "Add a secret…" is always the last row, including when the list is
    // empty: the answer to "the one I want is not here" belongs where the
    // question is asked, not in a settings page the user has to know about.
    // Its display text carries the typed filter, because that is almost
    // always the name they were reaching for.
    const createRow: FloatingPanelRow = {
      id: CREATE_ROW_ID,
      displayText: addSecretLabel(s.filter),
      matchRanges: [],
      group: GROUP_LABEL,
    }
    this.panel.show({
      rows: [
        ...rows.map((entry) => ({
          id: entry.id,
          displayText: entry.name,
          matchRanges: this.matchRange(entry.name, s.filter),
          group: GROUP_LABEL,
        })),
        createRow,
      ],
      selectedIndex: Math.min(s.selected, rows.length),
      footer: ['↑ ↓ to navigate', '↵ to insert', 'esc to dismiss'],
    })
  }

  private badge(label: string): HTMLElement {
    const b = document.createElement('span')
    b.className = 'ui-badge ui-floating-panel__source'
    b.dataset.tone = 'warning'
    b.textContent = label
    return b
  }

  /** The matched substring of a name — the same first-occurrence rule the
   *  recall filter uses, so the highlight is exact, never a heuristic. */
  private matchRange(name: string, filter: string): Array<{ from: number; to: number }> {
    if (filter === '') return []
    const at = name.toLowerCase().indexOf(filter.toLowerCase())
    return at === -1 ? [] : [{ from: at, to: at + filter.length }]
  }

  private pick(index: number): void {
    const s = this.state
    if (s.name !== 'list') return
    const rows = this.matches(s.entries, s.filter)
    const entry = rows[index]
    if (!entry) return
    this.close()
    this.callbacks.onInsert(entry.name)
  }
}
