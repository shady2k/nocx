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
    ['sealed', 'Unlock the vault to use its secrets'],
    ['uninitialized', 'Set up the vault to store secrets'],
  ] as const)('%s vault renders an offer row, not an error', async (state, label) => {
    const h = setup([], state)
    h.value.current = '@'
    h.controller.onInput('@', 1)
    await flush()

    expect(rows().map((row) => row.querySelector('.ui-collection-row__info')?.textContent)).toEqual(
      [label],
    )
    expect(rows()[0]?.dataset.empty).toBeUndefined()
    expect(rows()[0]?.querySelector('[data-tone="danger"]')).toBeNull()
  })

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
