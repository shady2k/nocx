// @vitest-environment jsdom
//
// The field a secret value is typed into (nocx-ew3uv.1), and the one
// property it exists to keep: THE VALUE PASSES THROUGH.
//
// A row marked secret used to say only that its value lives outside the file
// and offer no way to put one there — an import was the only minter, so a
// variable declared secret in this editor stayed unresolved for ever. This
// is the write. What every test below is really asking is where the value
// ends up: on the wire once, and in no signal, no row and no field
// afterwards.
import { describe, expect, it, afterEach, beforeEach, vi } from 'vitest'
import { render, cleanup, fireEvent } from '@solidjs/testing-library'
import { clearToasts, toasts } from '../ui/toast'
import { EnvironmentView, type ValueRow } from './environment-view'
import type { ApiRoute } from './api-model'

beforeEach(() => clearToasts())

afterEach(() => cleanup())

const DIRECT: ApiRoute = { kind: 'direct', profileId: '', insecureTls: false }
const VALUE = 'sk-live-9f2c4e7a11b3d8'
function mount(over: {
  rows?: ValueRow[]
  creating?: boolean
  error?: string
  onBindSecret?: (n: string, v: string) => Promise<void>
  route?: ApiRoute
  onRoute?: (route: ApiRoute) => void
}) {
  return render(() => (
    <EnvironmentView
      environments={[{ relPath: 'environments/dev.json', name: 'dev' }]}
      editing="environments/dev.json"
      active="environments/dev.json"
      creating={over.creating ?? false}
      name="dev"
      relPath="environments/dev.json"
      rows={over.rows ?? [{ name: 'token', value: '', secret: true }]}
      dirty={false}
      busy={false}
      error={over.error ?? ''}
      onPick={() => {}}
      onNew={() => {}}
      onName={() => {}}
      onRelPath={() => {}}
      onRows={() => {}}
      onSave={() => {}}
      onReset={() => {}}
      route={over.route ?? DIRECT}
      onRoute={over.onRoute ?? (() => {})}
      connections={[]}
      onBindSecret={over.onBindSecret}
    />
  ))
}

/** The field a value is typed into, when the editor offers one. */
const secretField = (): HTMLInputElement | null =>
  document.querySelector<HTMLInputElement>('#api-environment-var-secret-0')

const storeButton = (): HTMLButtonElement | undefined =>
  [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Store')

describe('a secret row can be given its value', () => {
  it('sends the value once, by variable name, and clears the field afterwards', async () => {
    const onBindSecret = vi.fn().mockResolvedValue(undefined)
    mount({ onBindSecret })

    const field = secretField()
    expect(field, 'the editor offers no field to type a secret value into').not.toBeNull()
    fireEvent.input(field!, { target: { value: VALUE } })
    fireEvent.click(storeButton()!)

    await vi.waitFor(() => expect(onBindSecret).toHaveBeenCalledWith('token', VALUE))
    expect(onBindSecret).toHaveBeenCalledTimes(1)

    // CLEARED, so the surface keeps no copy of a value the backend
    // deliberately does not answer back. A field still holding it would be
    // this component storing a credential nobody asked it to store.
    await vi.waitFor(() => expect(secretField()!.value).toBe(''))
    // …and the whole document holds no byte of it either — a value can ride
    // an attribute no text assertion would look at.
    expect(document.body.innerHTML).not.toContain(VALUE)
  })

  it('is a password field, because somebody may be looking at the screen', () => {
    mount({ onBindSecret: vi.fn().mockResolvedValue(undefined) })
    expect(secretField()!.type).toBe('password')
  })

  it('a refusal keeps what was typed and says why', async () => {
    // The opposite of the clear above, and the reason it is on success only:
    // emptying the field on a refusal costs the person the value they just
    // pasted, which they may not have anywhere else.
    const onBindSecret = vi.fn().mockRejectedValue(new Error('the vault is sealed'))
    const { container } = mount({ onBindSecret })

    fireEvent.input(secretField()!, { target: { value: VALUE } })
    fireEvent.click(storeButton()!)

    await vi.waitFor(() => expect(container.textContent).toContain('the vault is sealed'))
    expect(secretField()!.value).toBe(VALUE)
  })

  it('Store is refused while there is nothing to store', () => {
    mount({ onBindSecret: vi.fn().mockResolvedValue(undefined) })
    expect(storeButton()!.disabled).toBe(true)
    fireEvent.input(secretField()!, { target: { value: 'x' } })
    expect(storeButton()!.disabled).toBe(false)
  })

  it('with no binding capability the field is not drawn at all', () => {
    // Optionality IS the capability, the rule the folder pickers keep: a
    // build with nowhere to put a value must not offer somewhere to type
    // one. What is left is the sentence that was always there.
    const { container } = mount({})
    expect(secretField()).toBeNull()
    expect(storeButton()).toBeUndefined()
    expect(container.textContent).toContain('Bound in the vault')
  })

  it('a row with no name yet offers nothing to store it under', () => {
    // The binding key is a triple of names; a nameless row has no key, and a
    // field that took a value it could not address would be a value going
    // nowhere.
    mount({
      rows: [{ name: '  ', value: '', secret: true }],
      onBindSecret: vi.fn().mockResolvedValue(undefined),
    })
    expect(secretField()).toBeNull()
  })

  it('a PLAIN row still edits its value in the file, untouched by any of this', () => {
    mount({
      rows: [{ name: 'baseUrl', value: 'https://api.example.com', secret: false }],
      onBindSecret: vi.fn().mockResolvedValue(undefined),
    })
    expect(secretField()).toBeNull()
    const plain = document.querySelector<HTMLInputElement>('#api-environment-var-value-0')
    expect(plain?.value).toBe('https://api.example.com')
  })
})

describe('the outcome of a refused Save', () => {
  it('is said in a toast when there is no field the refusal belongs to', async () => {
    mount({ error: 'save refused on disk' })
    await vi.waitFor(() => expect(toasts()).toHaveLength(1))
    const told = toasts()[0]
    expect(told.level).toBe('danger')
    expect(told.message).toBe('save refused on disk')
  })

  it('stays on the path field while one is being made, and says no toast', () => {
    mount({ creating: true, error: 'a file with that name is already there' })
    const field = document.querySelector<HTMLInputElement>('#api-environment-path')
    expect(field?.getAttribute('aria-invalid')).toBe('true')
    expect(toasts()).toHaveLength(0)
  })
})

// THE CONTROL THAT TURNS VERIFICATION OFF (nocx-6hg2w.25).
//
// It had no test at all, and the words it wore were the defect: it read
// "Accept self-signed certificates" while what it sets is InsecureSkipVerify,
// which forgives every refusal a certificate can draw. Somebody refused for an
// authority this machine does not know read the label, concluded the product
// had no switch for their case, and asked for a second one — which would have
// been a second owner of one input.
describe('the switch that stops the certificate being checked', () => {
  /** Found the way a person finds it: by the words on it. */
  const verifySwitch = (): HTMLInputElement | undefined =>
    [...document.querySelectorAll('label.ui-checkbox')]
      .find((l) => /do not verify the server/i.test(l.textContent ?? ''))
      ?.querySelector('input') ?? undefined

  it('is named for the check it turns off, and turning it on reaches the route', () => {
    const onRoute = vi.fn()
    mount({ onRoute })

    const control = verifySwitch()
    expect(
      control,
      'no control on this page says it stops the certificate being verified',
    ).toBeDefined()
    expect(control!.checked).toBe(false)

    fireEvent.click(control!)
    expect(onRoute).toHaveBeenCalledWith({ ...DIRECT, insecureTls: true })
  })

  it('says which refusals it covers BEFORE it is on', () => {
    // The person who needs it is reading this page because a send was
    // refused, and the refusal they are holding is usually not the word
    // "self-signed". A list that appears only once the switch is on is a list
    // that cannot answer "is this my case?".
    const { container } = mount({})
    const text = container.textContent ?? ''
    expect(text).toMatch(/self-signed/i)
    expect(text).toMatch(/authority/i)
    expect(text).toMatch(/another name|other name|name it/i)
  })

  it('with it on, the page still says what every send under it now does', () => {
    const { container } = mount({ route: { ...DIRECT, insecureTls: true } })
    expect(verifySwitch()!.checked).toBe(true)
    expect(container.textContent).toContain('who it says it is')
  })
})
