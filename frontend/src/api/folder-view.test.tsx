// @vitest-environment jsdom
import { createSignal } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import type { SecretPickerSource } from '../ui/secret-picker'
import { FolderView } from './folder-view'

const SECRET = { id: 'secrow:abc123', name: 'Deploy token' }
const SOURCE: SecretPickerSource = {
  status: vi.fn().mockResolvedValue({ state: 'unsealed' }),
  list: vi.fn().mockResolvedValue([SECRET]),
  requestUnseal: vi.fn().mockResolvedValue(undefined),
  requestSetup: vi.fn().mockResolvedValue(false),
  requestCreate: vi.fn(),
}

afterEach(() => cleanup())

function mount(
  over: {
    variables?: Array<{ name: string; value: string; enabled: boolean }>
    onVariables?: (variables: readonly { name: string; value: string; enabled: boolean }[]) => void
    secretSource?: SecretPickerSource
    secretEntries?: readonly (typeof SECRET)[]
    vaultState?: 'uninitialized' | 'sealed' | 'unsealed' | 'unknown'
  } = {},
) {
  const [variables, setVariables] = createSignal(
    over.variables ?? [{ name: 'token', value: '', enabled: true }],
  )
  const onVariables = (
    next: readonly { name: string; value: string; enabled: boolean }[],
  ): void => {
    over.onVariables?.(next)
    setVariables([...next])
  }
  return render(() => (
    <FolderView
      folder="users"
      entries={[]}
      onOpen={() => {}}
      actions={() => <></>}
      variables={variables()}
      loading={false}
      busy={false}
      written={false}
      error=""
      saveError=""
      onVariables={onVariables}
      onNewRequest={() => {}}
      secretSource={over.secretSource}
      secretEntries={() => over.secretEntries ?? []}
      vaultState={() => over.vaultState ?? 'unknown'}
    />
  ))
}

const variablesTab = (): HTMLButtonElement =>
  document.querySelector<HTMLButtonElement>('button[role="tab"]:last-child')!

describe('folder variables', () => {
  it('keeps a plain value with an embedded @ ordinary', async () => {
    const onVariables = vi.fn()
    const source: SecretPickerSource = {
      ...SOURCE,
      status: vi.fn().mockResolvedValue({ state: 'unsealed' }),
    }
    mount({
      onVariables,
      secretSource: source,
      secretEntries: [SECRET],
      vaultState: 'unsealed',
    })
    fireEvent.click(variablesTab())

    const field = document.querySelector<HTMLInputElement>('#api-folder-var-value-0')!
    field.value = 'plain@value'
    field.setSelectionRange(field.value.length, field.value.length)
    fireEvent.input(field)

    expect(onVariables).toHaveBeenLastCalledWith([
      { name: 'token', value: 'plain@value', enabled: true },
    ])
    await vi.waitFor(() => expect(document.querySelector('.ui-floating-panel__row')).toBeNull())
  })

  it('lets @ insert a secret reference into a folder value', async () => {
    const onVariables = vi.fn()
    mount({ onVariables, secretSource: SOURCE, secretEntries: [SECRET], vaultState: 'unsealed' })
    fireEvent.click(variablesTab())

    const field = document.querySelector<HTMLInputElement>('#api-folder-var-value-0')!
    fireEvent.focus(field)
    field.value = '@deploy'
    field.setSelectionRange(field.value.length, field.value.length)
    fireEvent.input(field)
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-floating-panel__row')?.textContent).toContain(
        'Deploy token',
      ),
    )
    fireEvent.mouseDown(document.querySelector('.ui-floating-panel__row')!)

    await vi.waitFor(() =>
      expect(onVariables).toHaveBeenLastCalledWith([
        { name: 'token', value: '{{secret:secrow:abc123}}', enabled: true },
      ]),
    )
  })

  it('renders a reference chip with the display name and never the handle', () => {
    const { container } = mount({
      variables: [{ name: 'token', value: '{{secret:secrow:abc123}}', enabled: true }],
      secretSource: SOURCE,
      secretEntries: [SECRET],
      vaultState: 'unsealed',
    })
    fireEvent.click(variablesTab())

    const mark = container.querySelector('.ui-text-field__mark')
    expect(mark?.textContent).toBe('Deploy token')
    expect(mark?.textContent).not.toContain('secrow:abc123')
  })

  it('destroys the row picker when the row is removed', async () => {
    function Harness() {
      const [variables, setVariables] = createSignal([{ name: 'token', value: '', enabled: true }])
      return (
        <FolderView
          folder="users"
          entries={[]}
          onOpen={() => {}}
          actions={() => <></>}
          variables={variables()}
          loading={false}
          busy={false}
          written={false}
          error=""
          saveError=""
          onVariables={setVariables}
          onNewRequest={() => {}}
          secretSource={SOURCE}
          secretEntries={() => [SECRET]}
          vaultState={() => 'unsealed'}
        />
      )
    }

    render(() => <Harness />)
    fireEvent.click(variablesTab())
    expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(1)
    fireEvent.click(document.querySelector('button[aria-label="Remove variable 1"]')!)
    await vi.waitFor(() =>
      expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(
        0,
      ),
    )
  })

  it('keeps sealed and missing-handle messages distinct', () => {
    const sealed = mount({
      variables: [{ name: 'token', value: '{{secret:secrow:missing}}', enabled: true }],
      secretEntries: [],
      vaultState: 'sealed',
    })
    fireEvent.click(variablesTab())
    expect(sealed.container.textContent).toContain('Vault locked — unlock to view')
    cleanup()

    const unsealed = mount({
      variables: [{ name: 'token', value: '{{secret:secrow:missing}}', enabled: true }],
      secretEntries: [],
      vaultState: 'unsealed',
    })
    fireEvent.click(variablesTab())
    expect(unsealed.container.textContent).toContain('Secret not on this machine')
    expect(unsealed.container.textContent).not.toContain('Vault locked — unlock to view')
  })
})
