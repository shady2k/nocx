// @vitest-environment jsdom
/**
 * The ask form, exercised the way a person reaches it: a body arrives, the
 * form offers what that body declares, and what comes back out is the map
 * the fire is given.
 *
 * These assert the SEAM, not the render: `deps.fire`'s answers map is the
 * only thing the rest of the product sees, so every case ends by reading it
 * rather than by reading the DOM back.
 *
 * Controls are ACTED on by their label — the affordance a person uses — and
 * READ BACK through the id the form gives them, because a `.value` taken off
 * a `getByLabelText` result is taken off a type this project's config cannot
 * resolve, and an unchecked read is not an assertion.
 */
import { fireEvent, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountSnippetAskDialog, type SnippetAskDialogHandle } from './snippet-ask-dialog'
import type { Snippet } from './snippets-store'

const snippet = (body: string): Snippet => ({ id: 'i', title: 'T', body })

let open: SnippetAskDialogHandle | null = null

const mount = (fire: ReturnType<typeof vi.fn>): SnippetAskDialogHandle => {
  const host = document.createElement('div')
  document.body.append(host)
  const handle = mountSnippetAskDialog(host, { fire, onDelivered: () => {} })
  open = handle
  return handle
}

afterEach(() => {
  open?.dispose()
  open = null
  document.body.innerHTML = ''
})

/** The control the form gave this name, by the id it addresses it with. */
const control = <T extends HTMLElement>(name: string): T | null =>
  document.querySelector<T>(`#snippet-ask-${name}`)

/** A tick has no id of its own — the kit's Checkbox wraps its `<label>`
 *  round the input, and that is what names it. */
const tick = (name: string): HTMLInputElement | null => {
  for (const label of document.querySelectorAll('label.ui-checkbox')) {
    if (label.textContent?.trim() === name) return label.querySelector('input')
  }
  return null
}

const answersOf = (fire: ReturnType<typeof vi.fn>): ReadonlyMap<string, string> =>
  fire.mock.calls[0][1] as ReadonlyMap<string, string>

const insert = (): void => {
  const button = [...document.querySelectorAll('button')].find((b) => b.textContent === 'Insert')
  if (button === undefined) throw new Error('no Insert button on screen')
  fireEvent.click(button)
}

describe('the ask form', () => {
  it('offers a select for an option list, with the first option chosen', async () => {
    const fire = vi.fn(() => Promise.resolve(null))
    const h = mount(fire)
    h.ask(snippet('run {{w=claude|omp|codex}}'), 'input')

    await waitFor(() => expect(control('w')).toBeTruthy())
    const select = control<HTMLSelectElement>('w')
    expect(select?.tagName).toBe('SELECT')
    expect(select?.value).toBe('claude')
    expect([...(select?.options ?? [])].map((o) => o.value)).toEqual(['claude', 'omp', 'codex'])

    fireEvent.change(screen.getByLabelText('w'), { target: { value: 'codex' } })
    insert()
    await waitFor(() => expect(fire).toHaveBeenCalled())
    expect(answersOf(fire).get('w')).toBe('codex')
  })

  it('offers a tick for a condition, un-ticked, and sends what it means', async () => {
    const fire = vi.fn(() => Promise.resolve(null))
    const h = mount(fire)
    h.ask(snippet('a{% if fast %}!{% endif %}'), 'input')

    await waitFor(() => expect(tick('fast')).toBeTruthy())
    expect(tick('fast')?.type).toBe('checkbox')
    expect(tick('fast')?.checked).toBe(false)

    insert()
    await waitFor(() => expect(fire).toHaveBeenCalled())
    // An un-ticked flag is ANSWERED, not omitted: the resolver reads an
    // empty answer as "off" and a missing one as "not asked yet", and the
    // second would refuse a fire the person has finished filling in.
    expect(answersOf(fire).get('fast')).toBe('')
  })

  it('hides a field inside a block until its flag is ticked', async () => {
    const fire = vi.fn(() => Promise.resolve(null))
    const h = mount(fire)
    h.ask(snippet('{% if fast %}at {{n=3}}{% endif %}'), 'input')

    await waitFor(() => expect(tick('fast')).toBeTruthy())
    expect(control('n')).toBeNull()

    fireEvent.click(screen.getByLabelText('fast'))
    await waitFor(() => expect(control('n')).toBeTruthy())
    expect(control<HTMLInputElement>('n')?.value).toBe('3')

    insert()
    await waitFor(() => expect(fire).toHaveBeenCalled())
    expect(answersOf(fire).get('fast')).toBe('on')
    expect(answersOf(fire).get('n')).toBe('3')
  })

  it('un-ticking hides the question and keeps the typing, so re-ticking restores it', async () => {
    const fire = vi.fn(() => Promise.resolve(null))
    const h = mount(fire)
    h.ask(snippet('{% if fast %}at {{n=3}}{% endif %}'), 'input')

    await waitFor(() => expect(tick('fast')).toBeTruthy())
    fireEvent.click(screen.getByLabelText('fast'))
    await waitFor(() => expect(control('n')).toBeTruthy())
    fireEvent.input(screen.getByLabelText('n'), { target: { value: '9' } })

    fireEvent.click(screen.getByLabelText('fast'))
    await waitFor(() => expect(control('n')).toBeNull())

    fireEvent.click(screen.getByLabelText('fast'))
    await waitFor(() => expect(control('n')).toBeTruthy())
    expect(control<HTMLInputElement>('n')?.value).toBe('9')
  })

  it('asks nothing for a body with no fields', () => {
    const fire = vi.fn(() => Promise.resolve(null))
    const h = mount(fire)
    h.ask(snippet('git status'), 'input')
    expect(document.querySelector('dialog')).toBeNull()
  })

  it('a refusal keeps the form open beside the answers that caused it', async () => {
    const fire = vi.fn(() => Promise.resolve('the vault is locked'))
    const h = mount(fire)
    h.ask(snippet('psql {{db=prod}}'), 'input')

    await waitFor(() => expect(control('db')).toBeTruthy())
    fireEvent.input(screen.getByLabelText('db'), { target: { value: 'staging' } })
    insert()

    await waitFor(() => expect(screen.getByText('the vault is locked')).toBeTruthy())
    expect(control<HTMLInputElement>('db')?.value).toBe('staging')
  })
})
