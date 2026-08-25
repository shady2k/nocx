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
import { SecretPicker, type SecretEntry, type SecretPickerSource } from './secret-picker'

export interface SecretPickerFieldController {
  /** Call on every input event. Finds the trigger word around the caret and
   * drives the panel's filter, or closes it. */
  onInput(value: string, caret: number): void
  /** Open after an explicit menu/mark action inserted an @, regardless of
   *  the surrounding text. */
  openAt(caret: number): void
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
}): SecretPickerFieldController {
  interface Trigger {
    from: number
    to: number
    filter: string
  }

  let trigger: Trigger | null = null
  let generation = 0
  /** Escape dismisses the current @ word as ordinary text until the word
   * changes or disappears; a later input event must not resurrect its panel. */
  let dismissedTriggerFrom: number | null = null
  let loadedEntries: SecretEntry[] | null = null
  const idsByName = new Map<string, string>()

  const source: SecretPickerSource = {
    status: () => opts.source.status(),
    list: async () => {
      const entries = await opts.source.list()
      loadedEntries = entries
      idsByName.clear()
      for (const entry of entries) idsByName.set(entry.name, entry.id)
      return entries
    },
    requestUnseal: () => opts.source.requestUnseal(),
    requestSetup: () => opts.source.requestSetup(),
    requestCreate: (name) => opts.source.requestCreate(name),
  }

  const picker = new SecretPicker(source, {
    onInsert: (name) => {
      const currentTrigger = trigger
      const id = idsByName.get(name)
      if (currentTrigger === null || id === undefined) return

      const current = opts.value()
      const expected = `@${currentTrigger.filter}`
      if (current.slice(currentTrigger.from, currentTrigger.to) !== expected) {
        trigger = null
        picker.close()
        return
      }

      const reference = `{{secret:${id}}}`
      const next =
        current.slice(0, currentTrigger.from) + reference + current.slice(currentTrigger.to)
      const nextCaret = currentTrigger.from + reference.length
      trigger = null
      generation++
      opts.onChange(next, nextCaret)
    },
  })
  picker.mount(document.body)

  const close = (): void => {
    generation++
    trigger = null
    picker.close()
  }

  const closeIfNoMatch = (filter: string): void => {
    if (loadedEntries === null || loadedEntries.length === 0 || filter === '') return
    const needle = filter.toLowerCase()
    if (!loadedEntries.some((entry) => entry.name.toLowerCase().includes(needle))) close()
  }
  const openPicker = (filter: string): void => {
    if (!picker.isOpen) {
      const openingGeneration = generation
      void picker.open().then(() => {
        if (openingGeneration !== generation) {
          if (trigger === null) picker.close()
          return
        }
        if (trigger === null) {
          picker.close()
          return
        }
        closeIfNoMatch(trigger.filter)
      })
    }
    picker.setFilter(filter)
    closeIfNoMatch(filter)
  }


  const findTrigger = (value: string, caret: number): Trigger | null => {
    const end = Math.max(0, Math.min(value.length, caret))
    let start = end
    while (start > 0 && !/\s/.test(value[start - 1] ?? '') && value[start - 1] !== '@') start--
    if (start > 0 && value[start - 1] === '@') start--
    if (start >= end || value[start] !== '@') return null
    if (start > 0 && !/\s/.test(value[start - 1] ?? '')) return null
    return { from: start, to: end, filter: value.slice(start + 1, end) }
  }

  const onInput = (value: string, caret: number): void => {
    const nextTrigger = findTrigger(value, caret)
    if (nextTrigger === null) {
      dismissedTriggerFrom = null
      close()
      return
    }
    if (dismissedTriggerFrom !== null) {
      if (nextTrigger.from === dismissedTriggerFrom) {
        close()
        return
      }
      dismissedTriggerFrom = null
    }

    trigger = nextTrigger
    openPicker(nextTrigger.filter)
  }

  const openAt = (caret: number): void => {
    trigger = { from: Math.max(0, caret - 1), to: caret, filter: '' }
    dismissedTriggerFrom = null
    openPicker('')
  }

  const onKeyDown = (e: KeyboardEvent): boolean => {
    const wasOpen = picker.isOpen
    const consumed = picker.handleKey(e)
    if (consumed && wasOpen && !picker.isOpen) {
      // SecretPicker closes synchronously for Escape and accepted rows.
      // Row selection clears trigger in onInsert; Escape needs this adapter
      // state reset because it intentionally leaves the literal @ in place.
      dismissedTriggerFrom = trigger?.from ?? null
      generation++
      trigger = null
    }
    return consumed
  }

  const destroy = (): void => {
    generation++
    trigger = null
    dismissedTriggerFrom = null
    picker.destroy()
  }

  return {
    onInput,
    openAt,
    onKeyDown,
    close,
    destroy,
  }
}
