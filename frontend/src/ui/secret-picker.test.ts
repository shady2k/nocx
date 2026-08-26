// @vitest-environment jsdom
//
// The reference picker (ui/secret-picker.ts) — the PASSIVE '@' surface:
// one grouped list, offer rows for sealed/uninitialized, no-match closes
// silently, space closes, only Enter/Tab inserts. Keyboard-behavior tests
// drive handleKey directly (the editor's arbiter chain is terminal-content's
// wiring, tested there); rendering is asserted through the panel's DOM.
import { describe, it, expect, vi } from 'vitest'
import {
  SecretPicker,
  storeSecretLabel,
  STORE_LABEL_MAX,
  type SecretPickerSource,
  type SecretPickerCallbacks,
} from './secret-picker'
import type { VaultStatus, InventoryEntry } from '../vault-client'

function entry(name: string, id = name): InventoryEntry {
  return {
    id,
    name,
    kind: 'password',
    provider: 'file',
    ownerId: '',
    usedBy: 0,
    reachable: true,
  }
}

const UNSEALED: VaultStatus = {
  state: 'unsealed',
  osKeyAvailable: false,
  osKeyCapable: false,
  hasPassphrase: true,
  autoSealMinutes: 0,
  defaultProvider: 'file',
  providers: [],
}

interface Harness {
  picker: SecretPicker
  source: {
    status: ReturnType<typeof vi.fn>
    list: ReturnType<typeof vi.fn>
    requestUnseal: ReturnType<typeof vi.fn>
    requestCreate: ReturnType<typeof vi.fn>
    requestSetup: ReturnType<typeof vi.fn>
  }
  onInsert: ReturnType<typeof vi.fn>
  onError: (message: string, error: unknown) => unknown
  container: HTMLElement
}

function setup(status: VaultStatus = UNSEALED, entries: InventoryEntry[] = []): Harness {
  const source = {
    status: vi.fn(() => Promise.resolve(status)),
    list: vi.fn(() => Promise.resolve(entries)),
    requestUnseal: vi.fn(() => Promise.resolve()),
    requestCreate: vi.fn(),
    requestSetup: vi.fn(() => Promise.resolve(false)),
  } satisfies SecretPickerSource
  const onInsert = vi.fn()
  const onError = vi.fn()
  const callbacks: SecretPickerCallbacks = { onInsert, onError }
  const picker = new SecretPicker(source, callbacks)
  const container = document.createElement('div')
  document.body.appendChild(container)
  picker.mount(container)
  return { picker, source, onInsert, onError, container }
}

const flush = async (): Promise<void> => {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

function key(picker: SecretPicker, init: KeyboardEventInit): boolean {
  return picker.handleKey(
    new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
  )
}

const rows = (c: HTMLElement): Array<{ text: string; selected: boolean }> =>
  [...c.querySelectorAll<HTMLElement>('.ui-floating-panel__row')].map((el) => ({
    // The display text is the info column; the actions column (badges)
    // rides the same row's right edge and is asserted separately.
    text: el.querySelector('.ui-collection-row__info')?.textContent ?? '',
    selected: el.dataset.selected === 'true',
  }))

describe('SecretPicker: the list', () => {
  it('opens on an unsealed vault and lists every secret under the Secrets group', async () => {
    const h = setup(UNSEALED, [entry('openai-key'), entry('github-pat')])
    await h.picker.open()
    await flush()
    expect(h.picker.isOpen).toBe(true)
    const rowEls = rows(h.container)
    // "Add a secret…" is always the last row: the answer to "the one I
    // want is not here" belongs where the question is asked.
    expect(rowEls.map((r) => r.text)).toEqual(['openai-key', 'github-pat', 'Add a secret…'])
    expect(rowEls[0].selected).toBe(true)
    expect(h.container.querySelector('.ui-floating-panel__group')?.textContent).toBe('Secrets')
  })

  it('Enter inserts the selected name and closes', async () => {
    const h = setup(UNSEALED, [entry('openai-key'), entry('github-pat')])
    await h.picker.open()
    await flush()
    key(h.picker, { key: 'ArrowDown' })
    key(h.picker, { key: 'Enter' })
    expect(h.onInsert).toHaveBeenCalledWith('github-pat')
    expect(h.picker.isOpen).toBe(false)
  })

  it('Tab inserts like Enter', async () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    await h.picker.open()
    await flush()
    key(h.picker, { key: 'Tab' })
    expect(h.onInsert).toHaveBeenCalledWith('openai-key')
    expect(h.picker.isOpen).toBe(false)
  })

  it('arrows navigate and wrap', async () => {
    const h = setup(UNSEALED, [entry('a'), entry('b'), entry('c')])
    await h.picker.open()
    await flush()
    key(h.picker, { key: 'ArrowUp' }) // wrap to the last, which is the create row
    expect(rows(h.container).find((r) => r.selected)?.text).toBe('Add a secret…')
    key(h.picker, { key: 'ArrowUp' })
    expect(rows(h.container).find((r) => r.selected)?.text).toBe('c')
    key(h.picker, { key: 'ArrowDown' })
    key(h.picker, { key: 'ArrowDown' }) // back to the first
    expect(rows(h.container).find((r) => r.selected)?.text).toBe('a')
  })

  it('Esc closes and leaves everything else alone', async () => {
    const h = setup(UNSEALED, [entry('a')])
    await h.picker.open()
    await flush()
    key(h.picker, { key: 'Escape' })
    expect(h.picker.isOpen).toBe(false)
    expect(h.onInsert).not.toHaveBeenCalled()
  })

  it('a filter typed while the inventory is in flight wins over the open list', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const h = setup(UNSEALED, [entry('openai-key'), entry('github-pat')])
    h.source.list.mockImplementation(
      () =>
        new Promise((resolve) => {
          void gate.then(() => resolve([entry('openai-key'), entry('github-pat')]))
        }),
    )
    const opened = h.picker.open()
    h.picker.setFilter('github') // typed before the inventory landed
    release()
    await opened
    await flush()
    expect(rows(h.container).map((r) => r.text)).toEqual([
      'github-pat',
      'Add "github" to the vault…',
    ])
  })

  it('a no-match filter typed while loading lands on the create row', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const h = setup(UNSEALED, [entry('openai-key')])
    h.source.list.mockImplementation(
      () =>
        new Promise((resolve) => {
          void gate.then(() => resolve([entry('openai-key')]))
        }),
    )
    const opened = h.picker.open()
    h.picker.setFilter('zzz')
    release()
    await opened
    await flush()
    expect(h.picker.isOpen).toBe(true)
    expect(rows(h.container).map((r) => r.text)).toEqual(['Add "zzz" to the vault…'])
  })

  it('printable keys and space fall through to the line (the passive contract)', async () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    await h.picker.open()
    await flush()
    expect(key(h.picker, { key: 'o' })).toBe(false)
    expect(key(h.picker, { key: ' ' })).toBe(false)
    expect(key(h.picker, { key: ';' })).toBe(false)
    expect(h.picker.isOpen).toBe(true) // the CONTROLLER closes on the doc change
  })
})

describe('SecretPicker: the passive filter', () => {
  it('filters as the trigger word grows; a match is highlighted', async () => {
    const h = setup(UNSEALED, [entry('openai-key'), entry('openai-secret'), entry('github-pat')])
    await h.picker.open()
    await flush()
    h.picker.setFilter('openai')
    const rowEls = rows(h.container)
    expect(rowEls.map((r) => r.text)).toEqual([
      'openai-key',
      'openai-secret',
      'Add "openai" to the vault…',
    ])
    expect(h.container.querySelectorAll('.ui-floating-panel__match').length).toBe(2)
  })

  // A no-match used to close the panel, which was right while the panel
  // could only offer what the vault already held. Typing a name it does
  // NOT hold is exactly when "Add …" is the answer, and closing on that
  // keystroke took the offer away as the user reached for it.
  it('nothing matches -> stays open on the create row', async () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    await h.picker.open()
    await flush()
    h.picker.setFilter('zzz')
    expect(h.picker.isOpen).toBe(true)
    expect(rows(h.container).map((r) => r.text)).toEqual(['Add "zzz" to the vault…'])
    key(h.picker, { key: 'Enter' })
    expect(h.source.requestCreate).toHaveBeenCalledTimes(1)
  })
  it('inserts the row returned by an in-place create', async () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    h.source.requestCreate.mockResolvedValue(entry('brand-new', 'secrow:new'))
    await h.picker.open()
    await flush()
    h.picker.setFilter('brand-new')
    key(h.picker, { key: 'Enter' })
    await flush()
    expect(h.onInsert).toHaveBeenCalledWith('brand-new')
    expect(h.picker.isOpen).toBe(false)
  })

  it('a space in the filter closes (the trigger word ended)', async () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    await h.picker.open()
    await flush()
    h.picker.setFilter('open ')
    expect(h.picker.isOpen).toBe(false)
  })

  it('setFilter on a closed picker is a no-op', () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    h.picker.setFilter('open')
    expect(h.picker.isOpen).toBe(false)
  })
})

describe('SecretPicker: vault lifecycle states are OFFERS', () => {
  it('a sealed vault shows an unlock offer row, and Enter unseals then lists', async () => {
    const h = setup({ ...UNSEALED, state: 'sealed' }, [entry('openai-key')])
    await h.picker.open()
    await flush()
    expect(h.picker.isOpen).toBe(true)
    expect(rows(h.container).map((r) => r.text)).toEqual(['Unlock the vault to use its secrets'])
    key(h.picker, { key: 'Enter' })
    expect(h.source.requestUnseal).toHaveBeenCalledTimes(1)
    await flush()
    // The vault opened: the list loads in the same open picker session.
    expect(rows(h.container).map((r) => r.text)).toEqual(['openai-key', 'Add a secret…'])
  })

  it('an uninitialized vault offers to set up, and Enter sets up then lists', async () => {
    const h = setup({ ...UNSEALED, state: 'uninitialized' }, [entry('openai-key')])
    await h.picker.open()
    await flush()
    expect(rows(h.container).map((r) => r.text)).toEqual(['Set up the vault to store secrets'])
    key(h.picker, { key: 'Enter' })
    expect(h.source.requestSetup).toHaveBeenCalledTimes(1)
    await flush()
    expect(rows(h.container).map((r) => r.text)).toEqual(['openai-key', 'Add a secret…'])
  })

  // An empty vault is not a dead end when you are reaching for a secret —
  // it is exactly the moment to make one, and sending the user off to find
  // a settings page is how the feature goes unused.
  it('an unsealed vault with no secrets offers to create one', async () => {
    const h = setup(UNSEALED, [])
    await h.picker.open()
    await flush()
    expect(h.picker.isOpen).toBe(true)
    expect(rows(h.container).map((r) => r.text)).toEqual(['Add a secret…'])
    key(h.picker, { key: 'Enter' })
    expect(h.source.requestCreate).toHaveBeenCalledTimes(1)
    expect(h.picker.isOpen).toBe(false)
  })

  // Resolving a recalled command is the other question: what is missing
  // there is the key itself, and a create dialog is not the answer.
  it('an empty vault opened to RESOLVE says the key is missing instead', async () => {
    const h = setup(UNSEALED, [])
    await h.picker.open('resolve')
    await flush()
    expect(h.container.querySelector('[data-empty="true"]')?.textContent).toContain(
      'the key was removed from this command',
    )
  })

  it('a passphrase-required setup raises the dialog and CLOSES the panel (no stale row behind it)', async () => {
    const h = setup({ ...UNSEALED, state: 'uninitialized' }, [entry('openai-key')])
    h.source.requestSetup.mockResolvedValue(true) // the dialog took over
    await h.picker.open()
    await flush()
    key(h.picker, { key: 'Enter' })
    await flush()
    expect(h.source.requestSetup).toHaveBeenCalledTimes(1)
    expect(h.picker.isOpen).toBe(false) // the dialog is the surface now
    expect(h.onInsert).not.toHaveBeenCalled()
  })

  it('a refused unseal keeps the offer row on screen', async () => {
    const h = setup({ ...UNSEALED, state: 'sealed' }, [entry('openai-key')])
    h.source.requestUnseal.mockRejectedValue(new Error('cancelled'))
    await h.picker.open()
    await flush()
    key(h.picker, { key: 'Enter' })
    await flush()
    expect(rows(h.container).map((r) => r.text)).toEqual(['Unlock the vault to use its secrets'])
  })
  it('keeps a visible refusal row and logs when vault status is refused', async () => {
    const h = setup()
    const refusal = new Error('vault status unavailable')
    h.source.status.mockRejectedValue(refusal)
    await h.picker.open()
    await flush()
    expect(h.picker.isOpen).toBe(true)
    expect(
      h.container.querySelector<HTMLElement>('.ui-floating-panel__row')?.dataset.empty,
    ).toBeUndefined()
    expect(rows(h.container).map((r) => r.text)).toEqual([
      'Could not read vault secrets: vault status unavailable',
    ])
    expect(h.onError).toHaveBeenCalledWith('vault status unavailable', refusal)
  })
  it('keeps a visible refusal row and logs when inventory listing is refused', async () => {
    const h = setup()
    const refusal = new Error('inventory unavailable')
    h.source.list.mockRejectedValue(refusal)
    await h.picker.open()
    await flush()
    expect(h.picker.isOpen).toBe(true)
    expect(rows(h.container).map((r) => r.text)).toEqual([
      'Could not read vault secrets: inventory unavailable',
    ])
    expect(h.onError).toHaveBeenCalledWith('inventory unavailable', refusal)
  })
  it('keeps the sealed offer and logs when inventory reports the vault sealed', async () => {
    const h = setup()
    const refusal = Object.assign(new Error('vault is sealed'), {
      data: { reason: 'vault-sealed' },
    })
    h.source.list.mockRejectedValue(refusal)
    await h.picker.open()
    await flush()
    expect(rows(h.container).map((r) => r.text)).toEqual(['Unlock the vault to use its secrets'])
    expect(h.onError).toHaveBeenCalledWith('vault is sealed', refusal)
  })
})

// ── The store offer ──────────────────────────────────────────────────────
// The lock inside a field opens THIS panel, with one extra input: the text
// that would be stored. Nothing about that text decides what is offered —
// the panel never reads it except to name it (nocx-0khco ruled out guessing
// at credentials).
describe('SecretPicker: the store offer', () => {
  it('names the value it would store, truncated, and leaves a short one whole', () => {
    expect(storeSecretLabel('hello world')).toBe('Store "hello world" in the vault…')
    const long = 'sk-live-0123456789abcdefghijklmnopqrstuvwxyz'
    expect(long.length).toBeGreaterThan(STORE_LABEL_MAX)
    expect(storeSecretLabel(long)).toBe(`Store "${long.slice(0, STORE_LABEL_MAX)}…" in the vault…`)
    // A multiline selection must still read as one row.
    expect(storeSecretLabel('a\nb')).toBe('Store "a b" in the vault…')
  })

  it('puts the store row ABOVE the list, so an existing secret is still reachable', async () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    await h.picker.open('insert', { value: 'Bearer t.Yixxxx' })
    await flush()
    expect(rows(h.container).map((r) => r.text)).toEqual([
      'Store "Bearer t.Yixxxx" in the vault…',
      'openai-key',
      'Add a secret…',
    ])
    expect(rows(h.container)[0].selected).toBe(true)
  })

  it('opened without a store offer, the list is exactly what it was', async () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    await h.picker.open()
    await flush()
    expect(rows(h.container).map((r) => r.text)).toEqual(['openai-key', 'Add a secret…'])
  })

  it('Enter on the store row creates from that value and inserts the result', async () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    h.source.requestCreate.mockResolvedValue(entry('deploy', 'secrow:new'))
    await h.picker.open('insert', { value: 't.Yixxxx' })
    await flush()
    key(h.picker, { key: 'Enter' })
    expect(h.source.requestCreate).toHaveBeenCalledWith('', 't.Yixxxx')
    await flush()
    expect(h.onInsert).toHaveBeenCalledWith('deploy')
    expect(h.picker.isOpen).toBe(false)
  })

  it('the trailing create row still carries no value — it is the other act', async () => {
    const h = setup(UNSEALED, [])
    await h.picker.open('insert', { value: 't.Yixxxx' })
    await flush()
    // store row, then the create row (no entries)
    key(h.picker, { key: 'ArrowDown' })
    expect(rows(h.container).find((r) => r.selected)?.text).toBe('Add a secret…')
    key(h.picker, { key: 'Enter' })
    // One argument, not two: the create row has no value to carry, and
    // "absent" is how that is said.
    expect(h.source.requestCreate).toHaveBeenCalledWith('')
  })

  it('arrows reach the store row and wrap through it', async () => {
    const h = setup(UNSEALED, [entry('a')])
    await h.picker.open('insert', { value: 'v' })
    await flush()
    key(h.picker, { key: 'ArrowUp' })
    expect(rows(h.container).find((r) => r.selected)?.text).toBe('Add a secret…')
    key(h.picker, { key: 'ArrowDown' })
    expect(rows(h.container).find((r) => r.selected)?.text).toBe('Store "v" in the vault…')
  })

  it('a click takes the store row too (the lock is a mouse affordance)', async () => {
    const h = setup(UNSEALED, [entry('a')])
    await h.picker.open('insert', { value: 'v' })
    await flush()
    // The panel accepts a row on mousedown (floating-panel.ts).
    h.container
      .querySelectorAll<HTMLElement>('.ui-floating-panel__row')[0]
      .dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }))
    expect(h.source.requestCreate).toHaveBeenCalledWith('', 'v')
  })

  it('NOTHING about the value decides the rows: a sentence and a token get the same offer', async () => {
    const sentence = setup(UNSEALED, [entry('openai-key')])
    await sentence.picker.open('insert', { value: 'hello world' })
    await flush()
    const token = setup(UNSEALED, [entry('openai-key')])
    await token.picker.open('insert', { value: 'sk-live-9c1f' })
    await flush()
    expect(rows(sentence.container).map((r) => r.text)).toEqual([
      'Store "hello world" in the vault…',
      'openai-key',
      'Add a secret…',
    ])
    expect(rows(token.container).map((r) => r.text)).toEqual([
      'Store "sk-live-9c1f" in the vault…',
      'openai-key',
      'Add a secret…',
    ])
    expect(rows(sentence.container).length).toBe(rows(token.container).length)
  })

  it('a sealed vault offers to unlock first, and the store row survives the unseal', async () => {
    const h = setup({ ...UNSEALED, state: 'sealed' }, [entry('openai-key')])
    await h.picker.open('insert', { value: 'v' })
    await flush()
    expect(rows(h.container).map((r) => r.text)).toEqual(['Unlock the vault to use its secrets'])
    key(h.picker, { key: 'Enter' })
    await flush()
    expect(rows(h.container).map((r) => r.text)).toEqual([
      'Store "v" in the vault…',
      'openai-key',
      'Add a secret…',
    ])
  })

  it('the offer does not outlive its panel: the next open has no store row', async () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    await h.picker.open('insert', { value: 'v' })
    await flush()
    h.picker.close()
    await h.picker.open()
    await flush()
    expect(rows(h.container).map((r) => r.text)).toEqual(['openai-key', 'Add a secret…'])
  })
})

// TWO DOORS. Everything above is the PASSIVE door — '@' offers and never
// traps. The lock is the EXPLICIT door: a click IS a request, and answering
// a request with an offer row is the same defect as answering it with
// silence (secret-picker.ts header, the owner's decision of 2026-08-26).
describe('SecretPicker: the explicit door', () => {
  it('a sealed vault is ASKED to open — the unlock is raised, the list follows', async () => {
    const h = setup({ ...UNSEALED, state: 'sealed' }, [entry('openai-key')])
    await h.picker.open('insert', { value: 'v' }, 'explicit')
    await flush()

    expect(h.source.requestUnseal).toHaveBeenCalledTimes(1)
    expect(rows(h.container).map((r) => r.text)).toEqual([
      'Store "v" in the vault…',
      'openai-key',
      'Add a secret…',
    ])
  })

  it('a cancelled unlock closes and does nothing else: no offer row, no list, no insert', async () => {
    const h = setup({ ...UNSEALED, state: 'sealed' }, [entry('openai-key')])
    h.source.requestUnseal.mockRejectedValue(new Error('cancelled'))
    await h.picker.open('insert', { value: 'v' }, 'explicit')
    await flush()

    expect(h.picker.isOpen).toBe(false)
    expect(rows(h.container)).toEqual([])
    expect(h.source.list).not.toHaveBeenCalled()
    expect(h.onInsert).not.toHaveBeenCalled()
  })

  it('the passive door is untouched: the same sealed vault still only offers', async () => {
    const h = setup({ ...UNSEALED, state: 'sealed' }, [entry('openai-key')])
    await h.picker.open()
    await flush()

    expect(h.source.requestUnseal).not.toHaveBeenCalled()
    expect(rows(h.container).map((r) => r.text)).toEqual(['Unlock the vault to use its secrets'])
  })

  it('an uninitialized vault gets the setup surface from the explicit door', async () => {
    const h = setup({ ...UNSEALED, state: 'uninitialized' }, [entry('openai-key')])
    await h.picker.open('insert', undefined, 'explicit')
    await flush()

    expect(h.source.requestSetup).toHaveBeenCalledTimes(1)
    expect(rows(h.container).map((r) => r.text)).toEqual(['openai-key', 'Add a secret…'])
  })

  it('a setup dialog that takes the surface leaves no panel behind it', async () => {
    const h = setup({ ...UNSEALED, state: 'uninitialized' }, [entry('openai-key')])
    h.source.requestSetup.mockResolvedValue(true)
    await h.picker.open('insert', undefined, 'explicit')
    await flush()

    expect(h.picker.isOpen).toBe(false)
    expect(h.source.list).not.toHaveBeenCalled()
  })

  it('a refused setup closes rather than falling back to the offer row', async () => {
    const h = setup({ ...UNSEALED, state: 'uninitialized' }, [entry('openai-key')])
    h.source.requestSetup.mockRejectedValue(new Error('cancelled'))
    await h.picker.open('insert', undefined, 'explicit')
    await flush()

    expect(h.picker.isOpen).toBe(false)
    expect(rows(h.container)).toEqual([])
  })

  it('an uninitialized vault still only OFFERS through the passive door', async () => {
    const h = setup({ ...UNSEALED, state: 'uninitialized' }, [entry('openai-key')])
    await h.picker.open()
    await flush()

    expect(h.source.requestSetup).not.toHaveBeenCalled()
    expect(rows(h.container).map((r) => r.text)).toEqual(['Set up the vault to store secrets'])
  })

  it('nothing about the value decides it: no offer and a token-shaped one raise the same unlock', async () => {
    const bare = setup({ ...UNSEALED, state: 'sealed' }, [entry('openai-key')])
    await bare.picker.open('insert', undefined, 'explicit')
    await flush()
    const token = setup({ ...UNSEALED, state: 'sealed' }, [entry('openai-key')])
    await token.picker.open('insert', { value: 'sk-live-9c1f' }, 'explicit')
    await flush()

    expect(bare.source.requestUnseal).toHaveBeenCalledTimes(1)
    expect(token.source.requestUnseal).toHaveBeenCalledTimes(1)
    expect(rows(bare.container).map((r) => r.text)).toEqual(['openai-key', 'Add a secret…'])
    expect(rows(token.container).map((r) => r.text)).toEqual([
      'Store "sk-live-9c1f" in the vault…',
      'openai-key',
      'Add a secret…',
    ])
  })

  it('an unsealed vault opens identically through either door', async () => {
    const h = setup(UNSEALED, [entry('openai-key')])
    await h.picker.open('insert', undefined, 'explicit')
    await flush()

    expect(h.source.requestUnseal).not.toHaveBeenCalled()
    expect(h.source.requestSetup).not.toHaveBeenCalled()
    expect(rows(h.container).map((r) => r.text)).toEqual(['openai-key', 'Add a secret…'])
  })
})
