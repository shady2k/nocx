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
//
// TWO DOORS, and the difference between them is deliberate (the owner's
// decision, 2026-08-26). Everything above belongs to the PASSIVE door:
// typing '@' is not a request to open the vault, so raising a passphrase
// prompt over someone who is composing a line would trap them — the same
// reason a no-match '@' never becomes a mode. The field's lock is the
// EXPLICIT door: the click IS the request, and answering a request with an
// offer row is the same defect as answering it with silence (which is why
// Tab and '@' already differ above). So an explicit open raises the real
// surface — requestUnseal for a sealed vault, requestSetup for an
// uninitialized one — and lands on the list the person asked for.
//
// The seal state and which door was used are the WHOLE input. Nothing about
// the value being held decides any of it (nocx-0khco ruled out reading the
// value), and neither door weakens the other: '@' still only ever offers.
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
  /** The user activated the uninitialized offer row: the host raises the
   *  setup dialog. Resolves true when a dialog took over the surface (the
   *  panel closes — the dialog is the surface now), false when nothing did
   *  and the list can reload.
   *
   *  It is a boolean and not a void because it USED to have two answers: a
   *  machine whose OS keystore could carry the vault was set up silently and
   *  the panel stayed put. ADR-0050 step 1 removed that, so every
   *  implementation returns true today — the contract is kept because the
   *  panel's two behaviours are still the panel's to choose between. */
  requestSetup(): Promise<boolean>
  /** The user activated a create row. Form hosts return the newly
   *  created row so the field can insert its opaque handle in place; hosts
   *  without a value-entry form (the terminal prompt) return `undefined`
   *  after handing the person to their existing destination. The hosts differ
   *  because a form has somewhere to type a value while a floating row over a
   *  prompt does not.
   *
   *  `value` is the text the store row would keep — the whole field or the
   *  selection, decided by the host. It is OMITTED, never passed as
   *  `undefined`, by the plain "Add a secret…" row: that row has no value to
   *  carry, and the two rows are different acts. */
  requestCreate(name: string, value?: string): Promise<SecretEntry | undefined>
  /** Host logging seam for failures reading lifecycle or inventory state. */
  onError?: (message: string, error: unknown) => void
}

export interface SecretPickerCallbacks {
  /** Insert `{{secret:NAME}}` over the trigger word; the host owns the
   *  editor seam and the replacement range. */
  onInsert(name: string): void
  /** Report a refused picker read through the host's logger. */
  onError?(message: string, error: unknown): void
  /** The panel was dismissed outside the field's keyboard path. */
  onDismiss?(reason: 'escape' | 'outside'): void
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

/** How much of the value a store row spells out. Long enough to tell one
 *  token from another — and a selection from the whole field — at a glance,
 *  short enough that a row stays a row. */
export const STORE_LABEL_MAX = 32

/** The store offer, naming exactly what would be kept. It NAMES the text and
 *  reads nothing else out of it: no shape, prefix or entropy of the value
 *  decides what the panel offers (nocx-0khco ruled out guessing at
 *  credentials), so `hello world` and a live token get the same row. */
export function storeSecretLabel(value: string): string {
  // A selection can span lines; a row is one line. Collapsing whitespace is
  // display only — the stored text is the host's, untouched.
  const flat = value.replace(/\s+/g, ' ')
  const shown = flat.length > STORE_LABEL_MAX ? `${flat.slice(0, STORE_LABEL_MAX)}…` : flat
  return `Store "${shown}" in the vault…`
}

/** The synthetic last row of the list state: "Add a secret…". Not a vault
 *  entry, so it is addressed by a reserved id rather than by index alone. */
const CREATE_ROW_ID = '\u0000create'

/** The synthetic FIRST row of the list state, present only while the host
 *  offered a value to store. */
const STORE_ROW_ID = '\u0000store'

/** WHICH DOOR opened the panel, and the only thing that decides whether a
 *  locked vault is OFFERED a row or ASKED to open. 'passive' is the '@'
 *  trigger and every host that merely watches the document; 'explicit' is a
 *  control the person pressed to reach the vault — today the field's lock.
 *  See the header for why one panel serves both. */
export type SecretPickerDoor = 'passive' | 'explicit'

/** Why the picker is open. 'insert' is the '@' trigger — the person is
 *  composing and wants to reach for a secret. 'resolve' is a recalled
 *  command whose credential the store removed: the panel's job there is
 *  first to say what is missing, and only then to offer the vault. */
export type SecretPickerPurpose = 'insert' | 'resolve'

/** What a store row would put in the vault, and it is an ORTHOGONAL input to
 *  the purpose rather than a third member of it. Purpose answers "which
 *  question is this panel answering when it has nothing to list"; this
 *  answers "is there a value here worth keeping". They vary independently —
 *  a panel opened to insert may or may not have one — so folding the store
 *  context into the enum would multiply the states instead of describing
 *  them, and every `purpose === 'resolve'` sentence in here would have to
 *  learn about a case that has nothing to do with it. */
export interface SecretPickerStoreOffer {
  /** Exactly the characters that will be stored. The host owns the editor
   *  seam, so the host decides whether that is the whole field or a
   *  selection; the panel only names it. */
  value: string
}

/** One row of the list state, in the order it is rendered: the store offer
 *  (when the host gave one), then the matching entries, then the create row.
 *  `selected` indexes THIS list — one row vocabulary, so navigation, click
 *  and Enter cannot disagree about which row is which. */
type ListRow =
  | { readonly kind: 'store'; readonly value: string }
  | { readonly kind: 'entry'; readonly entry: SecretEntry }
  | { readonly kind: 'create' }

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
  /** The host's store offer for THIS opening; null when there is nothing to
   *  keep (an empty field, or an '@' trigger, which is the other direction). */
  private store: SecretPickerStoreOffer | null = null
  /** The controller's filter that arrived while the inventory was still in
   *  flight: typing `@ope` before the list lands must not render every
   *  secret unfiltered. Applied when the list renders; null when none. */
  private pendingFilter: string | null = null
  /** Invalidates a pending create result when the picker opens again. */
  private createRequest = 0
  /** Invalidates an in-flight EXPLICIT door — an unlock prompt or a setup
   *  run — when the panel closes or opens again underneath it. A cancelled
   *  prompt must not render into a panel that is no longer this one. */
  private session = 0
  private readonly panel: FloatingPanel

  constructor(
    private readonly source: SecretPickerSource,
    private readonly callbacks: SecretPickerCallbacks,
    /** The field this picker opens against, for a host that mounts the panel
     *  outside it (the plain-input adapter mounts on the body). Absent is the
     *  terminal: the prompt mounts the panel INTO the editor root, whose
     *  positioning is what places it, and passing an anchor there would
     *  replace a correct placement with a computed one. See FloatingPanel's
     *  `anchor`. */
    opts?: { anchor?: () => HTMLElement | null },
  ) {
    this.panel = new FloatingPanel({
      variant: 'secret',
      role: 'listbox',
      ariaLabel: 'vault secrets',
      callbacks: {
        onHover: (index) => this.hover(index),
        onPick: (index) => this.pick(index),
        onDismiss: (reason) => {
          // Close before notifying the host: the callback may clear the
          // trigger that opened this panel, but it must never observe a live
          // stale surface.
          this.close()
          this.callbacks.onDismiss?.(reason)
        },
      },
      ...(opts?.anchor !== undefined ? { anchor: opts.anchor } : {}),
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
  async open(
    purpose: SecretPickerPurpose = 'insert',
    store?: SecretPickerStoreOffer,
    door: SecretPickerDoor = 'passive',
  ): Promise<void> {
    if (this.isOpen) return
    this.purpose = purpose
    this.store = store ?? null
    this.createRequest++
    const session = ++this.session
    this.state = { name: 'loading' }
    this.render()
    try {
      const status = await this.source.status()
      if (status.state === 'sealed') {
        if (door === 'explicit') {
          await this.raiseUnseal(session)
          return
        }
        this.state = { name: 'sealed' }
        this.render()
        return
      }
      if (status.state === 'uninitialized') {
        if (door === 'explicit') {
          await this.raiseSetup(session)
          return
        }
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
    const next = { ...s, filter }
    this.state = { ...next, selected: Math.min(s.selected, this.listRows(next).length - 1) }
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
    this.session++
    this.state = { name: 'closed' }
    this.pendingFilter = null
    this.store = null
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
    const count = this.listRows(s).length
    const next = (s.selected + dir + count) % count
    this.state = { ...s, selected: next }
    this.render()
  }

  /** Mouse hover moves the selection — and a hover that moves it NOWHERE
   *  must not re-render.
   *
   *  `FloatingPanel.show` rebuilds the list, so a re-render replaces every row
   *  node. The fresh node lands under a cursor that has not moved and fires
   *  `mouseenter` again, which arrives here with the index that is already
   *  selected — and without this guard that is a loop: the list rebuilds
   *  itself under the pointer for as long as the pointer rests on it. It also
   *  makes a click on a row a race with the row's own hover handler, because
   *  the node a press began on can be replaced before the press finishes
   *  (nocx-vzdna: this is what Playwright reported as "element was detached
   *  from the DOM"). One re-render per actual change of selection is all the
   *  surface ever needed. */
  private hover(index: number): void {
    const s = this.state
    if (s.name !== 'list') return
    if (s.selected === index) return
    this.state = { ...s, selected: index }
    this.render()
  }

  /** Enter/Tab on a row: insert the reference (list), or act on the offer
   *  (sealed -> unseal, uninitialized -> set up). */
  private activate(): void {
    const s = this.state
    switch (s.name) {
      case 'list': {
        const row = this.listRows(s)[s.selected]
        if (row === undefined) return
        if (row.kind === 'entry') {
          this.close()
          this.callbacks.onInsert(row.entry.name)
          return
        }
        const typed = s.filter
        const request = ++this.createRequest
        // Read before close(): closing clears the store offer.
        const created =
          row.kind === 'store'
            ? this.source.requestCreate(typed, row.value)
            : this.source.requestCreate(typed)
        this.close()
        void Promise.resolve(created)
          .then((entry) => {
            if (request !== this.createRequest || entry === undefined) return
            this.callbacks.onInsert(entry.name)
          })
          .catch(() => {})
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

  /** The explicit door onto a sealed vault: ask, do not offer. Failures are
   *  swallowed here rather than reaching open()'s catch, because a cancelled
   *  prompt is not a refused read and must not become a 'could not read the
   *  vault' row. */
  private async raiseUnseal(session: number): Promise<void> {
    try {
      await this.source.requestUnseal()
    } catch {
      // Cancelled or refused. They asked and then said no; putting the same
      // unlock back up as an offer row would be asking a second time. Close
      // and change nothing — the host's value is untouched either way,
      // because only an accepted row ever calls onInsert.
      if (session === this.session) this.close()
      return
    }
    if (session !== this.session) return
    await this.loadList('')
  }

  /** The same door onto an uninitialized vault. The owner decided the click
   *  may raise setup for the reason it may raise the unlock: it is the same
   *  request, and only the vault's state differs. */
  private async raiseSetup(session: number): Promise<void> {
    let dialogTookOver: boolean
    try {
      dialogTookOver = await this.source.requestSetup()
    } catch {
      if (session === this.session) this.close()
      return
    }
    if (session !== this.session) return
    if (dialogTookOver) {
      // The setup dialog owns the surface now — the same rule the offer
      // row's path follows: nothing stays up behind it.
      this.close()
      return
    }
    await this.loadList('')
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
    const list = this.listRows(s)
    this.panel.show({
      rows: list.map((row) => this.rowOf(row, s.filter)),
      selectedIndex: Math.min(s.selected, list.length - 1),
      footer: ['↑ ↓ to navigate', '↵ to insert', 'esc to dismiss'],
    })
  }

  /** The rendered rows, in order — the ONE place the list's shape is
   *  decided, so navigation, click, Enter and render cannot disagree. */
  private listRows(s: { entries: SecretEntry[]; filter: string }): ListRow[] {
    const rows: ListRow[] = []
    // ABOVE the list: what the person is holding comes first, and the
    // secrets they already have stay one arrow away — the store row must
    // not stand between them and replacing the value with an existing one.
    if (this.store !== null) rows.push({ kind: 'store', value: this.store.value })
    for (const entry of this.matches(s.entries, s.filter)) rows.push({ kind: 'entry', entry })
    // "Add a secret…" is always the last row, including when the list is
    // empty: the answer to "the one I want is not here" belongs where the
    // question is asked, not in a settings page the user has to know about.
    // Its display text carries the typed filter, because that is almost
    // always the name they were reaching for.
    rows.push({ kind: 'create' })
    return rows
  }

  private rowOf(row: ListRow, filter: string): FloatingPanelRow {
    if (row.kind === 'store') {
      return {
        id: STORE_ROW_ID,
        displayText: storeSecretLabel(row.value),
        matchRanges: [],
        group: GROUP_LABEL,
      }
    }
    if (row.kind === 'create') {
      return {
        id: CREATE_ROW_ID,
        displayText: addSecretLabel(filter),
        matchRanges: [],
        group: GROUP_LABEL,
      }
    }
    return {
      id: row.entry.id,
      displayText: row.entry.name,
      matchRanges: this.matchRange(row.entry.name, filter),
      group: GROUP_LABEL,
    }
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

  /** A click is the same act as Enter on that row, and says so by BEING it:
   *  the lock is a mouse affordance, so the store row must be clickable, and
   *  a second dispatch here is how a click and Enter drift apart. */
  private pick(index: number): void {
    const s = this.state
    if (s.name === 'sealed' || s.name === 'uninitialized') {
      if (index === 0) this.activate()
      return
    }
    if (s.name !== 'list') return
    this.state = { ...s, selected: index }
    this.activate()
  }
}
