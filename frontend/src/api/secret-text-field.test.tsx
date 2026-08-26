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
  it('passes the selected range to the store callback without opening a panel', () => {
    const onStoreSelection = vi.fn()
    const { container } = render(() => (
      <SecretTextField
        id="api-header-value"
        value="Bearer token"
        onStoreSelection={onStoreSelection}
      />
    ))
    const input = container.querySelector<HTMLInputElement>('#api-header-value')!
    input.focus()
    input.setSelectionRange(7, 12)

    const action = screen.getByRole('button', { name: 'Store in vault' })
    fireEvent.click(action)

    expect(onStoreSelection).toHaveBeenCalledOnce()
    expect(onStoreSelection).toHaveBeenCalledWith({ start: 7, end: 12 })
    expect(document.activeElement).toBe(input)
  })
})
