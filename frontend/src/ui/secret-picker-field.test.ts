// @vitest-environment jsdom
//
// The plain-input adapter keeps SecretPicker passive over a native field:
// the field owns its value and caret, while the picker only offers rows.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createSecretPickerField, type SecretPickerFieldController } from './secret-picker-field'
import type { SecretPickerSource, SecretEntry } from './secret-picker'

const flush = async (): Promise<void> => {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

interface Harness {
  controller: SecretPickerFieldController
  source: {
    status: ReturnType<typeof vi.fn>
    list: ReturnType<typeof vi.fn>
    requestUnseal: ReturnType<typeof vi.fn>
    requestSetup: ReturnType<typeof vi.fn>
    requestCreate: ReturnType<typeof vi.fn>
  }
  value: { current: string }
  onChange: ReturnType<typeof vi.fn>
}

function entry(name: string, id: string): SecretEntry {
  return { name, id }
}

function setup(
  entries: SecretEntry[] = [entry('prod-key', 'secrow:prod-id')],
  state: 'uninitialized' | 'sealed' | 'unsealed' = 'unsealed',
): Harness {
  const source = {
    status: vi.fn(() => Promise.resolve({ state })),
    list: vi.fn(() => Promise.resolve(entries)),
    requestUnseal: vi.fn(() => Promise.resolve()),
    requestSetup: vi.fn(() => Promise.resolve(false)),
    requestCreate: vi.fn(),
  } satisfies SecretPickerSource
  const value = { current: '' }
  const onChange = vi.fn((next: string, caret: number) => {
    value.current = next
    return caret
  })
  const controller = createSecretPickerField({
    source,
    value: () => value.current,
    onChange,
  })
  return { controller, source, value, onChange }
}

function panel(): HTMLElement | null {
  return document.body.querySelector<HTMLElement>('.ui-floating-panel[data-variant="secret"]')
}

function rows(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('.ui-floating-panel__row')]
}

function key(controller: SecretPickerFieldController, init: KeyboardEventInit): boolean {
  return controller.onKeyDown(
    new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }),
  )
}
function typeCharacter(
  input: HTMLInputElement,
  controller: SecretPickerFieldController,
  char: string,
): void {
  input.addEventListener('keydown', (event) => {
    controller.onKeyDown(event)
  })
  input.addEventListener('input', () => {
    controller.onInput(input.value, input.selectionStart ?? input.value.length)
  })
  input.dispatchEvent(new KeyboardEvent('keydown', { key: char, bubbles: true, cancelable: true }))
  input.value += char
  input.dispatchEvent(new Event('input', { bubbles: true }))
}
afterEach(() => {
  document.body.replaceChildren()
})

describe('createSecretPickerField', () => {
  it('opens on a word-start @, preserves the literal, and lets the next key reach the field', async () => {
    const h = setup()
    h.value.current = '@'
    h.controller.onInput('@', 1)
    await flush()

    expect(panel()?.dataset.open).toBe('true')
    expect(h.value.current).toBe('@')

    h.value.current = '@p'
    expect(key(h.controller, { key: 'p' })).toBe(false)
    h.controller.onInput('@p', 2)
    expect(h.value.current).toBe('@p')
  })

  it('does not open when @ is inside a word', async () => {
    const h = setup()
    h.value.current = 'foo@bar'
    h.controller.onInput(h.value.current, h.value.current.length)
    await flush()

    expect(panel()?.dataset.open).not.toBe('true')
  })

  it('drives the panel filter from the trigger word', async () => {
    const h = setup([entry('openai-key', 'secrow:openai'), entry('github-pat', 'secrow:github')])
    h.value.current = '@'
    h.controller.onInput('@', 1)
    await flush()

    h.value.current = '@open'
    h.controller.onInput('@open', 5)
    expect(rows().map((row) => row.querySelector('.ui-collection-row__info')?.textContent)).toEqual(
      ['openai-key', 'Add "open" to the vault…'],
    )
  })

  it('closes when the trigger word gains a space', async () => {
    const h = setup()
    h.value.current = '@prod'
    h.controller.onInput('@prod', 5)
    await flush()

    h.value.current = '@prod value'
    h.controller.onInput(h.value.current, h.value.current.length)
    expect(panel()?.dataset.open).not.toBe('true')
  })

  it('Esc closes, leaves @ as text, and does not resurrect the old trigger', async () => {
    const h = setup()
    h.value.current = '@prod'
    h.controller.onInput('@prod', 5)
    await flush()

    expect(key(h.controller, { key: 'Escape' })).toBe(true)
    expect(panel()?.dataset.open).not.toBe('true')
    expect(h.value.current).toBe('@prod')
    expect(h.onChange).not.toHaveBeenCalled()

    h.value.current = '@prod-k'
    h.controller.onInput(h.value.current, h.value.current.length)
    await flush()
    expect(panel()?.dataset.open).not.toBe('true')
  })

  it('destroy removes its mounted panel from the document', () => {
    const h = setup()
    expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(1)

    h.controller.destroy()

    expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(0)
  })

  it('destroying two controllers in sequence leaves no picker panels', () => {
    const first = setup()
    const second = setup()
    expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(2)

    first.controller.destroy()
    expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(1)

    second.controller.destroy()
    expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(0)
  })

  it('a filter with no matching secret keeps the create row open', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = '@missing'
    h.controller.onInput(h.value.current, h.value.current.length)
    await flush()

    expect(panel()?.dataset.open).toBe('true')
    expect(rows().map((row) => row.querySelector('.ui-collection-row__info')?.textContent)).toEqual(
      ['Add "missing" to the vault…'],
    )
    expect(h.source.requestCreate).not.toHaveBeenCalled()
  })

  it('Enter replaces the trigger word with the opaque row handle and places the caret after it', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = 'use @prod-key now'
    h.controller.onInput(h.value.current, 13)
    await flush()

    expect(key(h.controller, { key: 'Enter' })).toBe(true)
    expect(h.onChange).toHaveBeenCalledWith(
      'use {{secret:secrow:prod-id}} now',
      'use {{secret:secrow:prod-id}}'.length,
    )
  })

  it('Tab accepts the selected row exactly like Enter', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = '@prod-key'
    h.controller.onInput(h.value.current, h.value.current.length)
    await flush()

    expect(key(h.controller, { key: 'Tab' })).toBe(true)
    expect(h.onChange).toHaveBeenCalledWith(
      '{{secret:secrow:prod-id}}',
      '{{secret:secrow:prod-id}}'.length,
    )
  })

  it('does not insert on any other key', async () => {
    const h = setup()
    h.value.current = '@'
    h.controller.onInput('@', 1)
    await flush()

    expect(key(h.controller, { key: 'x' })).toBe(false)
    expect(h.onChange).not.toHaveBeenCalled()
  })

  it.each([
    ['sealed', ['Unlock the vault to use its secrets'], []],
    ['uninitialized', ['Set up the vault to store secrets'], []],
    ['unsealed', ['prod-key', 'Add a secret…'], [entry('prod-key', 'secrow:prod-id')]],
  ] as const)(
    '%s vault renders its offer after typing a bare @',
    async (state, expected, entries) => {
      const h = setup([...entries], state)
      const input = document.createElement('input')
      document.body.appendChild(input)
      typeCharacter(input, h.controller, '@')
      await flush()

      expect(
        rows().map((row) => row.querySelector('.ui-collection-row__info')?.textContent),
      ).toEqual(expected)
    },
  )

  it('Add a secret calls requestCreate with exactly the unmatched text typed after @', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = '@brand-new'
    h.controller.onInput(h.value.current, h.value.current.length)
    await flush()

    expect(key(h.controller, { key: 'ArrowDown' })).toBe(true)
    expect(key(h.controller, { key: 'Enter' })).toBe(true)
    expect(h.source.requestCreate).toHaveBeenCalledWith('brand-new')
    expect(h.onChange).not.toHaveBeenCalled()
  })
  it('maps an in-place create result before inserting its opaque handle', async () => {
    const h = setup()
    h.value.current = '@brand-new'
    h.controller.onInput(h.value.current, h.value.current.length)
    await flush()
    h.source.requestCreate.mockResolvedValue(entry('brand-new', 'secrow:new'))
    expect(key(h.controller, { key: 'Enter' })).toBe(true)
    await flush()
    expect(h.onChange).toHaveBeenCalledWith('{{secret:secrow:new}}', '{{secret:secrow:new}}'.length)
  })

  it('does not insert a create result into a newer trigger range', async () => {
    let resolveCreate!: (created: SecretEntry) => void
    const h = setup()
    h.source.requestCreate.mockImplementation(
      () =>
        new Promise<SecretEntry>((resolve) => {
          resolveCreate = resolve
        }),
    )
    h.value.current = '@brand-new'
    h.controller.onInput(h.value.current, h.value.current.length)
    await flush()
    expect(key(h.controller, { key: 'Enter' })).toBe(true)

    h.value.current = '@different'
    h.controller.onInput(h.value.current, h.value.current.length)
    resolveCreate(entry('brand-new', 'secrow:new'))
    await flush()

    expect(h.value.current).toBe('@different')
    expect(h.onChange).not.toHaveBeenCalled()
  })

  it('preserves an existing reference byte-for-byte when inserting a second one elsewhere', async () => {
    const h = setup([entry('second', 'secrow:second-id')])
    h.value.current = 'first {{secret:secrow:first-id}} @second'
    h.controller.onInput(h.value.current, h.value.current.length)
    await flush()

    key(h.controller, { key: 'Enter' })
    expect(h.onChange).toHaveBeenCalledWith(
      'first {{secret:secrow:first-id}} {{secret:secrow:second-id}}',
      'first {{secret:secrow:first-id}} {{secret:secrow:second-id}}'.length,
    )
  })

  it('suppresses a stale async open after the field no longer has a trigger', async () => {
    let release!: (value: { state: 'unsealed' }) => void
    const h = setup()
    h.source.status.mockImplementation(
      () =>
        new Promise((resolve) => {
          release = resolve
        }),
    )
    h.value.current = '@'
    h.controller.onInput('@', 1)
    h.value.current = '@ '
    h.controller.onInput('@ ', 2)
    release({ state: 'unsealed' })
    await flush()

    expect(panel()?.dataset.open).not.toBe('true')
  })
})

// ── The lock: storing what is already in the field ───────────────────────
// The lock inside the field opens the SAME panel '@' opens. Two inputs
// decide what it offers: is the field empty, and is part of it selected.
describe('createSecretPickerField: the lock', () => {
  const labels = (): Array<string | null | undefined> =>
    rows().map((row) => row.querySelector('.ui-collection-row__info')?.textContent)

  it('an empty field offers the plain list — one panel, no store row', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = ''
    h.controller.openForStore({ start: 0, end: 0 })
    await flush()

    expect(panel()?.dataset.open).toBe('true')
    expect(labels()).toEqual(['prod-key', 'Add a secret…'])
  })

  it('a filled field with nothing selected offers to store the whole value, above the list', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = 'Bearer t.Yixxxx'
    h.source.requestCreate.mockResolvedValue(entry('deploy', 'secrow:new'))
    h.controller.openForStore({ start: 15, end: 15 })
    await flush()

    expect(labels()).toEqual(['Store "Bearer t.Yixxxx" in the vault…', 'prod-key', 'Add a secret…'])
    expect(key(h.controller, { key: 'Enter' })).toBe(true)
    expect(h.source.requestCreate).toHaveBeenCalledWith('', 'Bearer t.Yixxxx')
    await flush()
    expect(h.value.current).toBe('{{secret:secrow:new}}')
  })

  // THE PANEL CLOSES WHEN THE ROW IS TAKEN, and the ask answers afterwards
  // (secret-picker.ts, activate). Meanwhile the promise `open()` returned is
  // still in flight, and its guard asks "did this door settle closed?" — a
  // question whose honest answer here is no: the person chose. Before
  // nocx-3o0ed.4 the guard read the closed panel as a refusal and dropped the
  // span, so a create ask that took more than a tick — a dialog, which is
  // every real one — came back with a row that replaced nothing at all.
  it('a store row taken the instant it appears still lands, though the open settles after it', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = 'Bearer t.Yixxxx'
    let settle!: (created: SecretEntry) => void
    h.source.requestCreate.mockImplementation(
      () =>
        new Promise<SecretEntry>((resolve) => {
          settle = resolve
        }),
    )
    h.controller.openForStore({ start: 15, end: 15 })
    // Act the moment the row is on screen, which is what a click does and
    // what the open promise has not caught up with yet.
    const storeRow = 'Store "Bearer t.Yixxxx" in the vault\u2026'
    for (let i = 0; i < 20 && labels()[0] !== storeRow; i++) await Promise.resolve()
    expect(labels()[0]).toBe(storeRow)
    expect(key(h.controller, { key: 'Enter' })).toBe(true)

    // A dialog's worth of ticks passes, and the guard resolves inside them.
    await flush()
    settle(entry('deploy', 'secrow:new'))
    await flush()

    expect(h.value.current).toBe('{{secret:secrow:new}}')
  })

  it('a selection stores ONLY the selection and replaces ONLY that span', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = 'Bearer t.Yixxxx'
    h.source.requestCreate.mockResolvedValue(entry('deploy', 'secrow:new'))
    h.controller.openForStore({ start: 7, end: 15 })
    await flush()

    expect(labels()).toEqual(['Store "t.Yixxxx" in the vault…', 'prod-key', 'Add a secret…'])
    expect(key(h.controller, { key: 'Enter' })).toBe(true)
    expect(h.source.requestCreate).toHaveBeenCalledWith('', 't.Yixxxx')
    await flush()
    // The literal `Bearer ` survives as text: it was never selected.
    expect(h.onChange).toHaveBeenCalledWith(
      'Bearer {{secret:secrow:new}}',
      'Bearer {{secret:secrow:new}}'.length,
    )
    expect(h.value.current).toBe('Bearer {{secret:secrow:new}}')
  })

  it('the list under the store row still replaces what was typed', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = 'Bearer t.Yixxxx'
    h.controller.openForStore({ start: 15, end: 15 })
    await flush()

    expect(key(h.controller, { key: 'ArrowDown' })).toBe(true)
    expect(key(h.controller, { key: 'Enter' })).toBe(true)
    expect(h.value.current).toBe('{{secret:secrow:prod-id}}')
    expect(h.source.requestCreate).not.toHaveBeenCalled()
  })

  it('a value that looks nothing like a credential gets exactly the same rows', async () => {
    const plain = setup([entry('prod-key', 'secrow:prod-id')])
    plain.value.current = 'hello world'
    plain.controller.openForStore({ start: 0, end: 11 })
    await flush()
    const plainRows = labels()
    plain.controller.destroy()
    document.body.replaceChildren()

    const token = setup([entry('prod-key', 'secrow:prod-id')])
    token.value.current = 'sk-live-9c1f'
    token.controller.openForStore({ start: 0, end: 12 })
    await flush()
    const tokenRows = labels()

    expect(plainRows).toEqual(['Store "hello world" in the vault…', 'prod-key', 'Add a secret…'])
    expect(tokenRows).toEqual(['Store "sk-live-9c1f" in the vault…', 'prod-key', 'Add a secret…'])
  })

  it('a field changed under the open panel is not replaced', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = 'Bearer t.Yixxxx'
    let resolveCreate!: (created: SecretEntry) => void
    h.source.requestCreate.mockImplementation(
      () =>
        new Promise<SecretEntry>((resolve) => {
          resolveCreate = resolve
        }),
    )
    h.controller.openForStore({ start: 7, end: 15 })
    await flush()
    expect(key(h.controller, { key: 'Enter' })).toBe(true)

    h.value.current = 'Bearer something-else'
    resolveCreate(entry('deploy', 'secrow:new'))
    await flush()

    expect(h.value.current).toBe('Bearer something-else')
    expect(h.onChange).not.toHaveBeenCalled()
  })

  it('the lock re-opens over a panel the @ trigger left open', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = '@'
    h.controller.onInput('@', 1)
    await flush()
    expect(labels()).toEqual(['prod-key', 'Add a secret…'])

    h.value.current = 'Bearer t.Yixxxx'
    h.controller.openForStore({ start: 7, end: 15 })
    await flush()
    expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(1)
    expect(labels()).toEqual(['Store "t.Yixxxx" in the vault…', 'prod-key', 'Add a secret…'])
  })
})

// Criterion 1 says the empty-field panel is "the existing secrets, narrowable
// by TYPING". The lock leaves no '@' in the field, so there is no trigger word
// for findTrigger to find — these are the tests that say what typing does after
// the lock, on an empty field and on a filled one.
describe('createSecretPickerField: typing after the lock', () => {
  const labels = (): Array<string | null | undefined> =>
    rows().map((row) => row.querySelector('.ui-collection-row__info')?.textContent)

  it('an empty field narrows as the person types', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id'), entry('github-tok', 'secrow:gh')])
    h.value.current = ''
    h.controller.openForStore({ start: 0, end: 0 })
    await flush()
    expect(labels()).toEqual(['prod-key', 'github-tok', 'Add a secret…'])

    h.value.current = 'p'
    h.controller.onInput('p', 1)
    await flush()

    expect(panel()?.dataset.open).toBe('true')
    expect(labels()).toEqual(['prod-key', 'Add "p" to the vault…'])
  })

  it('and the row it narrowed to replaces the typed text, not just the caret', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id'), entry('github-tok', 'secrow:gh')])
    h.value.current = ''
    h.controller.openForStore({ start: 0, end: 0 })
    await flush()
    h.value.current = 'pro'
    h.controller.onInput('pro', 3)
    await flush()

    expect(key(h.controller, { key: 'Enter' })).toBe(true)
    expect(h.value.current).toBe('{{secret:secrow:prod-id}}')
  })

  it('a space ends the typed word and closes, exactly as it does after @', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = ''
    h.controller.openForStore({ start: 0, end: 0 })
    await flush()
    h.value.current = 'p '
    h.controller.onInput('p ', 2)
    await flush()

    expect(panel()?.dataset.open).not.toBe('true')
  })

  it('an @ typed after the lock is an ordinary mention trigger, not a second anchor', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = ''
    h.controller.openForStore({ start: 0, end: 0 })
    await flush()
    h.value.current = '@prod'
    h.controller.onInput('@prod', 5)
    await flush()

    expect(labels()).toEqual(['prod-key', 'Add "prod" to the vault…'])
    expect(key(h.controller, { key: 'Enter' })).toBe(true)
    expect(h.value.current).toBe('{{secret:secrow:prod-id}}')
  })

  it('a FILLED field closes instead: the keystroke changed the span being offered', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')])
    h.value.current = 'Bearer t.Yixxxx'
    h.controller.openForStore({ start: 7, end: 15 })
    await flush()
    expect(labels()).toEqual(['Store "t.Yixxxx" in the vault…', 'prod-key', 'Add a secret…'])

    // Typing over a selection is what a native input does: the offered text
    // is no longer in the field at all.
    h.value.current = 'Bearer p'
    h.controller.onInput('Bearer p', 8)
    await flush()

    expect(panel()?.dataset.open).not.toBe('true')
  })
})

// The lock is an EXPLICIT request, and the '@' beside it is still passive.
// The two doors differ on purpose: see the header of secret-picker.ts.
describe('createSecretPickerField: the lock asks, the @ offers', () => {
  const labels = (): Array<string | null | undefined> =>
    rows().map((row) => row.querySelector('.ui-collection-row__info')?.textContent)

  it('a sealed vault is asked to unlock, and the store row survives it', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')], 'sealed')
    h.value.current = 'Bearer t.Yixxxx'
    h.controller.openForStore({ start: 7, end: 15 })
    await flush()
    await flush()

    expect(h.source.requestUnseal).toHaveBeenCalledTimes(1)
    expect(labels()).toEqual(['Store "t.Yixxxx" in the vault…', 'prod-key', 'Add a secret…'])
  })

  it('a cancelled unlock loses nothing — the field keeps every character', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')], 'sealed')
    h.source.requestUnseal.mockRejectedValue(new Error('cancelled'))
    h.value.current = 'Bearer t.Yixxxx'
    h.controller.openForStore({ start: 7, end: 15 })
    await flush()
    await flush()

    expect(h.value.current).toBe('Bearer t.Yixxxx')
    expect(h.onChange).not.toHaveBeenCalled()
    expect(panel()?.dataset.open).not.toBe('true')
  })

  it('and the keystroke after that refusal does not re-raise the prompt', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')], 'sealed')
    h.source.requestUnseal.mockRejectedValue(new Error('cancelled'))
    h.value.current = ''
    h.controller.openForStore({ start: 0, end: 0 })
    await flush()
    await flush()

    h.value.current = 'p'
    h.controller.onInput('p', 1)
    await flush()

    expect(h.source.requestUnseal).toHaveBeenCalledTimes(1)
    expect(panel()?.dataset.open).not.toBe('true')
    expect(h.value.current).toBe('p')
  })

  it('a bare @ over the same sealed vault still only offers', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')], 'sealed')
    h.value.current = '@'
    h.controller.onInput('@', 1)
    await flush()
    await flush()

    expect(h.source.requestUnseal).not.toHaveBeenCalled()
    expect(labels()).toEqual(['Unlock the vault to use its secrets'])
  })

  it('an uninitialized vault gets setup from the lock, an offer row from @', async () => {
    const asked = setup([entry('prod-key', 'secrow:prod-id')], 'uninitialized')
    asked.value.current = 'Bearer t.Yixxxx'
    asked.controller.openForStore({ start: 7, end: 15 })
    await flush()
    await flush()
    expect(asked.source.requestSetup).toHaveBeenCalledTimes(1)
    expect(labels()).toEqual(['Store "t.Yixxxx" in the vault…', 'prod-key', 'Add a secret…'])
    asked.controller.destroy()

    const offered = setup([entry('prod-key', 'secrow:prod-id')], 'uninitialized')
    offered.value.current = '@'
    offered.controller.onInput('@', 1)
    await flush()
    await flush()
    expect(offered.source.requestSetup).not.toHaveBeenCalled()
    expect(labels()).toEqual(['Set up the vault to store secrets'])
  })

  it('nothing about the value decides it: an empty field asks to unlock too', async () => {
    const h = setup([entry('prod-key', 'secrow:prod-id')], 'sealed')
    h.value.current = ''
    h.controller.openForStore({ start: 0, end: 0 })
    await flush()
    await flush()

    expect(h.source.requestUnseal).toHaveBeenCalledTimes(1)
    expect(labels()).toEqual(['prod-key', 'Add a secret…'])
  })
})
