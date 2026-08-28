// SecretPickerField — the plain-input adapter for the passive SecretPicker.
//
// A field stores the opaque vault row handle in its reference:
// `{{secret:secrow:…}}` uses InventoryEntry.id, never InventoryEntry.name.
// The handle survives a rename because the name is display-only; a handle
// minted on another machine simply has no local inventory row and therefore
// cannot reach that machine's secrets.
//
// The adapter owns only the field's trigger span and replacement. SecretPicker
// remains the one owner of the passive panel, lifecycle offers, filtering, and
// keyboard acceptance rules.
import {
  SecretPicker,
  type SecretPickerDoor,
  type SecretPickerSource,
  type SecretPickerStoreOffer,
} from './secret-picker'

export interface SecretPickerFieldController {
  /** Call on every input event. Finds the trigger word around the caret and
   * drives the panel's filter, or closes it. */
  onInput(value: string, caret: number): void
  /** Open after an explicit menu/mark action inserted an @, regardless of
   *  the surrounding text. */
  openAt(caret: number): void
  /** The field's lock was activated. Opens the SAME panel '@' opens, with
   *  one extra offer on top: store what the field is holding. Two inputs
   *  decide what that is, and nothing else does — is the field empty, and is
   *  part of it selected. A selection stores and replaces only that span; no
   *  selection means the whole value; an empty field has nothing to store, so
   *  the panel is exactly the '@' panel.
   *
   *  It is the EXPLICIT door (secret-picker.ts, SecretPickerDoor): pressing
   *  the lock is a request to reach the vault, so a sealed one is asked to
   *  unlock and an uninitialized one is asked to set up, rather than being
   *  offered a row. '@' through onInput keeps the offer. */
  openForStore(selection: { start: number; end: number }): void
  /** Call on keydown. Returns true when the panel consumed the key. */
  onKeyDown(e: KeyboardEvent): boolean
  close(): void
  /** Destroy the controller and remove its mounted picker. The caller that
   * creates a controller owns its lifetime and must call this when the field
   * is removed. */
  destroy(): void
}

export function createSecretPickerField(opts: {
  source: SecretPickerSource
  value: () => string
  onChange: (next: string, caret: number) => void
  /** The field's own element, read afresh each time the panel is placed.
   *
   *  The panel is mounted on the body rather than inside the field (a plain
   *  input has no positioned root, and a form row is inside scrolling panes
   *  and a dialog that would clip it), so the body mount is what makes the
   *  panel float — and it is also what took the field's position away from
   *  it. Before this, the panel resolved its `bottom: 100%` against the
   *  initial containing block and opened above the top of the window, on
   *  every field there is (nocx-vzdna). Handing the element back is what
   *  lets FloatingPanel put the panel next to the field it belongs to.
   *
   *  A host with no element to give gets the old body-relative placement;
   *  no such host exists today, and this stays optional only so the kit
   *  never demands a DOM handle from a host that has none. */
  anchor?: () => HTMLElement | null
}): SecretPickerFieldController {
  interface Trigger {
    from: number
    to: number
    filter: string
    /** The characters the range must STILL hold for a replacement to be
     *  honest — `@filter` for the '@' trigger, the stored text for the lock.
     *  One field for both because there is one question: is this still the
     *  span the panel was opened over? */
    expected: string
  }

  let trigger: Trigger | null = null
  let generation = 0
  /** Escape dismisses the current @ word as ordinary text until the word
   * changes or disappears; a later input event must not resurrect its panel. */
  let dismissedTriggerFrom: number | null = null
  /** Where the typed word starts when the LOCK opened the panel over an empty
   *  field. There is no '@' in the field then — the lock leaves no sentinel —
   *  so `findTrigger` has nothing to find and every keystroke would look like
   *  "the trigger word is gone" and close the panel. This anchor is what makes
   *  the empty-field panel narrowable by typing (criterion 1: it IS the '@'
   *  panel, minus the '@').
   *
   *  Null whenever the panel is not in that state — and deliberately null for
   *  a lock over a FILLED field: see onInput. */
  let lockAnchor: number | null = null
  /** How many create/store asks this adapter is waiting on.
   *
   *  SecretPicker CLOSES the panel the moment such a row is activated and
   *  answers the ask afterwards (secret-picker.ts, activate). That close is
   *  not the "settled closed" the open-guard below is written for — it is the
   *  person having chosen — so the span must survive it, or the reference the
   *  ask returns replaces nothing and the store silently does not happen. The
   *  window is exactly as long as the ask: a create dialog is many ticks, and
   *  the guard's promise can resolve inside it (nocx-3o0ed.4). */
  let asksInFlight = 0
  const idsByName = new Map<string, string>()

  const source: SecretPickerSource = {
    status: () => opts.source.status(),
    list: async () => {
      const entries = await opts.source.list()
      idsByName.clear()
      for (const entry of entries) idsByName.set(entry.name, entry.id)
      return entries
    },
    onError: opts.source.onError,
    requestUnseal: () => opts.source.requestUnseal(),
    requestSetup: () => opts.source.requestSetup(),
    requestCreate: async (name, value) => {
      const requestedGeneration = generation
      asksInFlight++
      // Absent, not `undefined`: the plain create row has no value to carry
      // (secret-picker.ts, SecretPickerSource.requestCreate).
      const created = await (
        value === undefined
          ? opts.source.requestCreate(name)
          : opts.source.requestCreate(name, value)
      ).finally(() => {
        asksInFlight--
      })
      // The dialog may close after the field changed or was removed. Do not
      // make a late row addressable or let it replace a newer @ trigger.
      if (created === undefined || requestedGeneration !== generation || trigger === null) {
        return undefined
      }
      // The create result is not in the list snapshot that populated this
      // map. Add it before SecretPicker asks onInsert to replace the trigger.
      idsByName.set(created.name, created.id)
      return created
    },
  }

  const onPickerDismiss = (reason: 'escape' | 'outside'): void => {
    if (reason === 'escape') dismissedTriggerFrom = trigger?.from ?? null
    else dismissedTriggerFrom = null
    generation++
    trigger = null
    lockAnchor = null
  }
  const picker = new SecretPicker(
    source,
    {
      onInsert: (name) => {
        const currentTrigger = trigger
        const id = idsByName.get(name)
        if (currentTrigger === null || id === undefined) return

        const current = opts.value()
        if (current.slice(currentTrigger.from, currentTrigger.to) !== currentTrigger.expected) {
          trigger = null
          lockAnchor = null
          picker.close()
          return
        }

        const reference = `{{secret:${id}}}`
        const next =
          current.slice(0, currentTrigger.from) + reference + current.slice(currentTrigger.to)
        const nextCaret = currentTrigger.from + reference.length
        trigger = null
        lockAnchor = null
        generation++
        opts.onChange(next, nextCaret)
      },
      onError: opts.source.onError,
      onDismiss: onPickerDismiss,
    },
    // The panel lives on the body; the anchor is how it finds its way back
    // to the field it belongs to (see `anchor` above).
    ...(opts.anchor !== undefined ? [{ anchor: opts.anchor }] : []),
  )
  picker.mount(document.body)

  const close = (): void => {
    generation++
    trigger = null
    lockAnchor = null
    picker.close()
  }

  // This adapter serves the insert purpose. SecretPicker keeps its create row
  // visible for a name no vault entry matches; the resolve purpose may close
  // silently, but a plain field has something to offer here.
  const openPicker = (
    filter: string,
    store?: SecretPickerStoreOffer,
    door: SecretPickerDoor = 'passive',
  ): void => {
    if (!picker.isOpen) {
      const openingGeneration = generation
      void picker.open('insert', store, door).then(() => {
        if (openingGeneration !== generation) {
          if (trigger === null) picker.close()
          return
        }
        if (trigger === null) {
          picker.close()
          return
        }
        if (!picker.isOpen && asksInFlight === 0) {
          // The explicit door can settle CLOSED — a cancelled unlock, or a
          // setup dialog that took the surface. The span this panel was
          // opened over went with it, so the adapter drops it too: an anchor
          // left behind would re-open the panel on the very next keystroke,
          // which is the person being asked again after they said no.
          //
          // A pending ask is the OTHER way this panel is closed and not
          // refused (asksInFlight): the row was taken and its answer is still
          // coming, so the span is still wanted.
          trigger = null
          lockAnchor = null
        }
      })
    }
    picker.setFilter(filter)
  }

  const findTrigger = (value: string, caret: number): Trigger | null => {
    const end = Math.max(0, Math.min(value.length, caret))
    let start = end
    while (start > 0 && !/\s/.test(value[start - 1] ?? '') && value[start - 1] !== '@') start--
    if (start > 0 && value[start - 1] === '@') start--
    if (start >= end || value[start] !== '@') return null
    if (start > 0 && !/\s/.test(value[start - 1] ?? '')) return null
    const filter = value.slice(start + 1, end)
    return { from: start, to: end, filter, expected: `@${filter}` }
  }

  /** The typed word of a lock-opened panel over an empty field: everything
   *  between the anchor and the caret. Null when that is not the state, or
   *  when the word has ended — a space closes the panel here for the same
   *  reason it closes it after a '@' (SecretPicker.setFilter): whitespace is
   *  what "I am not naming a secret any more" looks like. */
  const lockTrigger = (value: string, caret: number): Trigger | null => {
    if (lockAnchor === null) return null
    const end = Math.max(0, Math.min(value.length, caret))
    if (end < lockAnchor) return null
    const filter = value.slice(lockAnchor, end)
    if (/\s/.test(filter)) return null
    return { from: lockAnchor, to: end, filter, expected: filter }
  }

  const onInput = (value: string, caret: number): void => {
    const nextTrigger = findTrigger(value, caret)
    if (nextTrigger === null) {
      const typed = lockTrigger(value, caret)
      if (typed !== null) {
        generation++
        trigger = typed
        openPicker(typed.filter)
        return
      }
      dismissedTriggerFrom = null
      close()
      return
    }
    // An '@' typed after the lock is an ordinary mention: one anchor at a
    // time, and findTrigger's is the one that owns a span containing a '@'.
    lockAnchor = null
    if (dismissedTriggerFrom !== null) {
      if (nextTrigger.from === dismissedTriggerFrom) {
        close()
        return
      }
      dismissedTriggerFrom = null
    }

    generation++
    // A changed trigger is a new replacement target. This invalidates any
    // create dialog result that belongs to the previous range.
    trigger = nextTrigger
    openPicker(nextTrigger.filter)
  }

  const openAt = (caret: number): void => {
    generation++
    lockAnchor = null
    trigger = { from: Math.max(0, caret - 1), to: caret, filter: '', expected: '@' }
    dismissedTriggerFrom = null
    openPicker('')
  }

  const openForStore = (selection: { start: number; end: number }): void => {
    // A panel left open by the '@' trigger is answering a different
    // question over a different span. One panel, one span: close it first.
    close()
    const value = opts.value()
    const clamp = (n: number): number => Math.max(0, Math.min(value.length, n))
    const start = clamp(Math.min(selection.start, selection.end))
    const end = clamp(Math.max(selection.start, selection.end))
    // Nothing selected means the whole value — the person pressed the lock
    // with a caret somewhere in the thing they want kept, and "the thing" is
    // all of it. An empty field has neither, and gets the plain list.
    const from = start === end ? 0 : start
    const to = start === end ? value.length : end
    const stored = value.slice(from, to)
    dismissedTriggerFrom = null
    generation++
    trigger = { from, to, filter: '', expected: stored }
    // WHAT TYPING DOES NEXT, and it is decided here by whether there is
    // anything to store:
    //
    //   Nothing to store (the field is empty) — this panel is the '@' panel
    //   with no '@' in it, so typing must narrow it. The anchor gives the
    //   typed word a start, and the word replaces itself with the reference
    //   when a row is taken, exactly as '@prod' does.
    //
    //   Something to store (the field is filled) — NO anchor, so the next
    //   keystroke closes the panel, and that is the right answer rather than
    //   a gap. The characters land IN the span being offered: typing over a
    //   selection replaces it, and typing at a caret changes the whole value
    //   the no-selection case offered. Either way the text named on the store
    //   row is no longer what the field holds, so a panel that stayed up
    //   would be offering to keep something that is gone — and would then
    //   fail its own staleness check at the moment of the click (onInsert
    //   compares the span against `expected`), which is a control that looks
    //   alive and silently does nothing. Closing says it at the keystroke.
    lockAnchor = stored === '' ? from : null
    openPicker('', stored === '' ? undefined : { value: stored }, 'explicit')
  }

  const onKeyDown = (e: KeyboardEvent): boolean => {
    const wasOpen = picker.isOpen
    const consumed = picker.handleKey(e)
    if (consumed && wasOpen && !picker.isOpen && e.key === 'Escape') {
      // SecretPicker closes synchronously; this path keeps the same adapter
      // reset as the document-owned dismissal while leaving @ as text.
      onPickerDismiss('escape')
    }
    return consumed
  }

  const destroy = (): void => {
    generation++
    trigger = null
    lockAnchor = null
    dismissedTriggerFrom = null
    picker.destroy()
  }

  return {
    onInput,
    openAt,
    openForStore,
    onKeyDown,
    close,
    destroy,
  }
}
