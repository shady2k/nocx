// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { For } from 'solid-js'
import {
  SecretTextField,
  secretMarks,
  type SecretEntry,
  type VaultState,
} from './secret-text-field'
import type { TextFieldMark } from '../ui/text-field'

afterEach(() => cleanup())

describe('the extracted secret text field seam', () => {
  it('maps missing references correctly across all vault states', () => {
    const entries: SecretEntry[] = [{ id: 'secrow:known', name: 'Deploy token' }]
    const resolvedValue = '{{secret:secrow:known}}'
    const missingValue = '{{secret:secrow:missing}}'
    const states: VaultState[] = ['uninitialized', 'sealed', 'unsealed', 'unknown']
    const expectedMissing: Record<VaultState, TextFieldMark> = {
      uninitialized: {
        from: 0,
        to: missingValue.length,
        tone: 'reference',
        secretHandle: 'secrow:missing',
      },
      sealed: {
        from: 0,
        to: missingValue.length,
        tone: 'secret',
        displayText: 'Vault locked — unlock to view',
        secretHandle: 'secrow:missing',
      },
      unsealed: {
        from: 0,
        to: missingValue.length,
        tone: 'unknown',
        displayText: 'Secret not on this machine',
        secretHandle: 'secrow:missing',
      },
      unknown: {
        from: 0,
        to: missingValue.length,
        tone: 'reference',
        secretHandle: 'secrow:missing',
      },
    }

    const resolved = secretMarks(resolvedValue, entries, 'unsealed')
    expect(resolved).toEqual([
      {
        from: 0,
        to: resolvedValue.length,
        tone: 'secret',
        displayText: 'Deploy token',
        secretHandle: 'secrow:known',
      },
    ])

    const missingMarks = states.map((state) => {
      const marks = secretMarks(missingValue, entries, state)
      expect(marks).toEqual([expectedMissing[state]])
      return marks
    })

    render(() => (
      <>
        <SecretTextField id="resolved" value={resolvedValue} marks={resolved} />
        <For each={states}>
          {(state, index) => (
            <SecretTextField
              id={`missing-${state}`}
              value={missingValue}
              marks={missingMarks[index()]}
            />
          )}
        </For>
      </>
    ))

    expect(
      [...document.querySelectorAll('.ui-text-field__mark')].map((mark) => mark.textContent),
    ).toEqual([
      'Deploy token',
      missingValue,
      'Vault locked — unlock to view',
      'Secret not on this machine',
      missingValue,
    ])
  })
})

describe('SecretTextField vault affordance', () => {
  // T1 shipped this lock reporting the selection to a callback that opened
  // nothing. It opens the one SecretPicker now — the same panel '@' opens.
  const source = () => ({
    status: vi.fn(() => Promise.resolve({ state: 'unsealed' as const })),
    list: vi.fn(() => Promise.resolve([{ id: 'secrow:prod-id', name: 'prod-key' }])),
    requestUnseal: vi.fn(() => Promise.resolve()),
    requestSetup: vi.fn(() => Promise.resolve(false)),
    requestCreate: vi.fn(),
  })

  const rowText = (): Array<string | null | undefined> =>
    [...document.querySelectorAll('.ui-floating-panel__row')].map(
      (row) => row.querySelector('.ui-collection-row__info')?.textContent,
    )

  const clickLock = (
    container: HTMLElement,
    id: string,
    selection: { start: number; end: number },
  ): HTMLInputElement => {
    const input = container.querySelector<HTMLInputElement>(`#${id}`)!
    input.focus()
    input.setSelectionRange(selection.start, selection.end)
    fireEvent.click(screen.getByRole('button', { name: 'Store in vault' }))
    return input
  }

  it('the lock opens the one picker, offering to store the selection', async () => {
    const { container } = render(() => (
      <SecretTextField id="api-header-value" value="Bearer t.Yixxxx" source={source()} />
    ))
    const input = clickLock(container, 'api-header-value', { start: 7, end: 15 })

    await vi.waitFor(() =>
      expect(rowText()).toEqual(['Store "t.Yixxxx" in the vault…', 'prod-key', 'Add a secret…']),
    )
    expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(1)
    expect(document.activeElement).toBe(input)
  })

  it('an empty field gets the plain list, with nothing to store', async () => {
    const { container } = render(() => (
      <SecretTextField id="api-empty-value" value="" source={source()} />
    ))
    clickLock(container, 'api-empty-value', { start: 0, end: 0 })

    await vi.waitFor(() => expect(rowText()).toEqual(['prod-key', 'Add a secret…']))
  })

  // The lock is the EXPLICIT door onto the vault (secret-picker.ts header).
  // A sealed vault is exactly when it is needed, so it is exactly when it
  // must be there — and pressing it asks, rather than offering a row.
  const sealed = () => ({
    ...source(),
    status: vi.fn(() => Promise.resolve({ state: 'sealed' as const })),
  })

  it('the lock is there while the vault is SEALED — the moment it is needed', () => {
    const { container } = render(() => (
      <SecretTextField id="api-sealed-visible" value="Bearer t.Yixxxx" source={sealed()} />
    ))
    container.querySelector<HTMLInputElement>('#api-sealed-visible')!.focus()

    expect(screen.getByRole('button', { name: 'Store in vault' })).toBeTruthy()
  })

  it('clicking it on a sealed vault raises the unlock and then lists', async () => {
    const vault = sealed()
    const { container } = render(() => (
      <SecretTextField id="api-sealed-value" value="Bearer t.Yixxxx" source={vault} />
    ))
    clickLock(container, 'api-sealed-value', { start: 7, end: 15 })

    await vi.waitFor(() => expect(vault.requestUnseal).toHaveBeenCalledTimes(1))
    await vi.waitFor(() =>
      expect(rowText()).toEqual(['Store "t.Yixxxx" in the vault…', 'prod-key', 'Add a secret…']),
    )
  })

  it('a refused unlock leaves the typed value exactly where it was', async () => {
    const vault = sealed()
    vault.requestUnseal.mockRejectedValue(new Error('cancelled'))
    const onInput = vi.fn()
    const { container } = render(() => (
      <SecretTextField
        id="api-sealed-refused"
        value="Bearer t.Yixxxx"
        source={vault}
        onInput={onInput}
      />
    ))
    const input = clickLock(container, 'api-sealed-refused', { start: 7, end: 15 })

    await vi.waitFor(() => expect(vault.requestUnseal).toHaveBeenCalledTimes(1))
    await vi.waitFor(() =>
      expect(
        document.querySelector<HTMLElement>('.ui-floating-panel[data-variant="secret"]')?.dataset
          .open,
      ).not.toBe('true'),
    )
    expect(input.value).toBe('Bearer t.Yixxxx')
    expect(onInput).not.toHaveBeenCalled()
    expect(vault.list).not.toHaveBeenCalled()
  })

  it('no source, no lock — a control that can do nothing is not offered', () => {
    render(() => <SecretTextField id="api-no-vault" value="Bearer t.Yixxxx" />)
    const input = document.querySelector<HTMLInputElement>('#api-no-vault')!
    input.focus()
    expect(screen.queryByRole('button', { name: 'Store in vault' })).toBeNull()
  })
})
