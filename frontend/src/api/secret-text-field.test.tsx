// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render } from '@solidjs/testing-library'
import { SecretTextField, secretMarks, type SecretEntry } from './secret-text-field'

afterEach(() => cleanup())

describe('the extracted secret text field seam', () => {
  it('keeps reference marks and chip text identical across all vault states', () => {
    const entries: SecretEntry[] = [{ id: 'secrow:known', name: 'Deploy token' }]
    const resolvedValue = '{{secret:secrow:known}}'
    const sealedValue = '{{secret:secrow:missing}}'
    const unknownValue = '{{secret:secrow:missing}}'
    const resolved = secretMarks(resolvedValue, entries, 'unsealed')
    const sealed = secretMarks(sealedValue, entries, 'sealed')
    const unknown = secretMarks(unknownValue, entries, 'unsealed')

    expect(resolved).toEqual([
      {
        from: 0,
        to: resolvedValue.length,
        tone: 'secret',
        displayText: 'Deploy token',
        secretHandle: 'secrow:known',
      },
    ])
    expect(sealed).toEqual([
      {
        from: 0,
        to: sealedValue.length,
        tone: 'secret',
        displayText: 'Vault locked — unlock to view',
        secretHandle: 'secrow:missing',
      },
    ])
    expect(unknown).toEqual([
      {
        from: 0,
        to: unknownValue.length,
        tone: 'unknown',
        displayText: 'Secret not on this machine',
        secretHandle: 'secrow:missing',
      },
    ])

    render(() => (
      <>
        <SecretTextField id="resolved" value={resolvedValue} marks={resolved} />
        <SecretTextField id="sealed" value={sealedValue} marks={sealed} />
        <SecretTextField id="unknown" value={unknownValue} marks={unknown} />
      </>
    ))

    expect([...document.querySelectorAll('.ui-text-field__mark')].map((mark) => mark.textContent)).toEqual([
      'Deploy token',
      'Vault locked — unlock to view',
      'Secret not on this machine',
    ])
  })
})
