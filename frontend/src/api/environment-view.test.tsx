// @vitest-environment jsdom
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { clearToasts, toasts } from '../ui/toast'
import type { SecretPickerSource } from '../ui/secret-picker'
import { EnvironmentView, toStored, type ValueRow } from './environment-view'
import type { ApiRoute } from './api-model'

beforeEach(() => clearToasts())
afterEach(() => cleanup())

const DIRECT: ApiRoute = { kind: 'direct', profileId: '', insecureTls: false }
const SECRET = { id: 'secrow:abc123', name: 'Deploy token' }
const SOURCE: SecretPickerSource = {
  status: vi.fn().mockResolvedValue({ state: 'unsealed' }),
  list: vi.fn().mockResolvedValue([SECRET]),
  requestUnseal: vi.fn().mockResolvedValue(undefined),
  requestSetup: vi.fn().mockResolvedValue(false),
  requestCreate: vi.fn(),
}

function mount(over: {
  rows?: ValueRow[]
  creating?: boolean
  error?: string
  route?: ApiRoute
  onRoute?: (route: ApiRoute) => void
  onRows?: (rows: readonly ValueRow[]) => void
  secretSource?: SecretPickerSource
  secretEntries?: readonly typeof SECRET[]
  vaultState?: 'uninitialized' | 'sealed' | 'unsealed' | 'unknown'
} = {}) {
  const [rows, setRows] = createSignal<ValueRow[]>(over.rows ?? [{ name: 'token', value: '' }])
  const onRows = (next: readonly ValueRow[]): void => {
    over.onRows?.(next)
    setRows([...next])
  }
  return render(() => (
    <EnvironmentView
      environments={[{ relPath: 'environments/dev.json', name: 'dev' }]}
      editing="environments/dev.json"
      active="environments/dev.json"
      creating={over.creating ?? false}
      name="dev"
      relPath="environments/dev.json"
      rows={rows()}
      dirty={false}
      busy={false}
      error={over.error ?? ''}
      onPick={() => {}}
      onNew={() => {}}
      onName={() => {}}
      onRelPath={() => {}}
      onRows={onRows}
      onSave={() => {}}
      onReset={() => {}}
      route={over.route ?? DIRECT}
      onRoute={over.onRoute ?? (() => {})}
      connections={[]}
      secretSource={over.secretSource}
      secretEntries={() => over.secretEntries ?? []}
      vaultState={() => over.vaultState ?? 'unknown'}
    />
  ))
}

describe('environment variables', () => {
  it('stores a secret reference as an ordinary value', () => {
    const bytes = JSON.stringify(
      toStored([{ name: 'token', value: '{{secret:secrow:abc123}}' }]),
    )

    expect(bytes).toContain('{{secret:secrow:abc123}}')
    expect(JSON.parse(bytes)).toEqual({
      values: { token: '{{secret:secrow:abc123}}' },
    })
  })

  it('has exactly Name and Value columns', () => {
    const { container } = mount()
    expect(
      [...container.querySelectorAll('th')]
        .filter((cell) => cell.querySelector('.ui-row-list__sr') === null)
        .map((cell) => cell.textContent?.trim()),
    ).toEqual(['Name', 'Value'])
  })
  it('keeps a plain value with an embedded @ ordinary', async () => {
    const onRows = vi.fn()
    const source: SecretPickerSource = {
      ...SOURCE,
      status: vi.fn().mockResolvedValue({ state: 'unsealed' }),
    }
    mount({ onRows, secretSource: source, secretEntries: [SECRET], vaultState: 'unsealed' })

    const field = document.querySelector<HTMLInputElement>('#api-environment-var-value-0')!
    field.value = 'plain@value'
    field.setSelectionRange(field.value.length, field.value.length)
    fireEvent.input(field)

    expect(onRows).toHaveBeenLastCalledWith([{ name: 'token', value: 'plain@value' }])
    await vi.waitFor(() => expect(document.querySelector('.ui-floating-panel__row')).toBeNull())
  })


  it('lets @ insert the opaque reference into a value cell', async () => {
    const onRows = vi.fn()
    mount({ onRows, secretSource: SOURCE, secretEntries: [SECRET], vaultState: 'unsealed' })

    const field = document.querySelector<HTMLInputElement>('#api-environment-var-value-0')!
    fireEvent.focus(field)
    field.value = '@deploy'
    field.setSelectionRange(field.value.length, field.value.length)
    fireEvent.input(field)
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-floating-panel__row')?.textContent).toContain('Deploy token'),
    )
    fireEvent.mouseDown(document.querySelector('.ui-floating-panel__row')!)

    await vi.waitFor(() =>
      expect(onRows).toHaveBeenLastCalledWith([
        { name: 'token', value: '{{secret:secrow:abc123}}' },
      ]),
    )
  })

  it('renders the display name for a reference without rendering its handle', () => {
    const { container } = mount({
      rows: [{ name: 'token', value: '{{secret:secrow:abc123}}' }],
      secretSource: SOURCE,
      secretEntries: [SECRET],
      vaultState: 'unsealed',
    })

    const mark = container.querySelector('.ui-text-field__mark')
    expect(mark?.textContent).toBe('Deploy token')
    expect(mark?.textContent).not.toContain('secrow:abc123')
  })

  it('distinguishes sealed from an unsealed vault with a missing handle', () => {
    const sealed = mount({
      rows: [{ name: 'token', value: '{{secret:secrow:missing}}' }],
      secretEntries: [],
      vaultState: 'sealed',
    })
    expect(sealed.container.textContent).toContain('Vault locked — unlock to view')
    cleanup()

    const unsealed = mount({
      rows: [{ name: 'token', value: '{{secret:secrow:missing}}' }],
      secretEntries: [],
      vaultState: 'unsealed',
    })
    expect(unsealed.container.textContent).toContain('Secret not on this machine')
    expect(unsealed.container.textContent).not.toContain('Vault locked — unlock to view')
  })

  it('destroys the row picker when the row is removed', async () => {
    function Harness() {
      const [rows, setRows] = createSignal<ValueRow[]>([{ name: 'token', value: '' }])
      return (
        <EnvironmentView
          environments={[{ relPath: 'environments/dev.json', name: 'dev' }]}
          editing="environments/dev.json"
          active="environments/dev.json"
          creating={false}
          name="dev"
          relPath="environments/dev.json"
          rows={rows()}
          dirty={false}
          busy={false}
          error=""
          onPick={() => {}}
          onNew={() => {}}
          onName={() => {}}
          onRelPath={() => {}}
          onRows={setRows}
          onSave={() => {}}
          onReset={() => {}}
          route={DIRECT}
          onRoute={() => {}}
          connections={[]}
          secretSource={SOURCE}
          secretEntries={() => [SECRET]}
          vaultState={() => 'unsealed'}
        />
      )
    }

    render(() => <Harness />)
    expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(1)
    fireEvent.click(document.querySelector('button[aria-label="Remove variable 1"]')!)
    await vi.waitFor(() =>
      expect(document.querySelectorAll('.ui-floating-panel[data-variant="secret"]')).toHaveLength(0),
    )
  })
})

describe('the switch that stops the certificate being checked', () => {
  const verifySwitch = (): HTMLInputElement | undefined =>
    [...document.querySelectorAll('label.ui-checkbox')]
      .find((label) => /do not verify the server/i.test(label.textContent ?? ''))
      ?.querySelector('input') ?? undefined

  it('is named for the check it turns off, and turning it on reaches the route', () => {
    const onRoute = vi.fn()
    mount({ onRoute })
    const control = verifySwitch()
    expect(control).toBeDefined()
    expect(control!.checked).toBe(false)
    fireEvent.click(control!)
    expect(onRoute).toHaveBeenCalledWith({ ...DIRECT, insecureTls: true })
  })

  it('says which refusals it covers before it is on', () => {
    const { container } = mount({})
    expect(container.textContent).toMatch(/self-signed/i)
    expect(container.textContent).toMatch(/authority/i)
    expect(container.textContent).toMatch(/another name|other name|name it/i)
  })

  it('with it on, the page still says what every send under it now does', () => {
    const { container } = mount({ route: { ...DIRECT, insecureTls: true } })
    expect(verifySwitch()!.checked).toBe(true)
    expect(container.textContent).toContain('who it says it is')
  })

  it('reports a refused save through the toast channel', async () => {
    mount({ error: 'save refused on disk' })
    await vi.waitFor(() => expect(toasts()).toHaveLength(1))
    expect(toasts()[0]).toMatchObject({ level: 'danger', message: 'save refused on disk' })
  })
})
