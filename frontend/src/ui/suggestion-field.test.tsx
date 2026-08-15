// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { SuggestionField, type SuggestionFieldProps } from './suggestion-field'

afterEach(() => cleanup())

/** A controlled harness that echoes every onInput into the value — the same
 *  round trip the product makes (updateModel → new row object → value prop),
 *  so the guarded mirror and the list's reactions to a changing value are
 *  exercised for real. */
function harness(
  overrides?: Partial<SuggestionFieldProps> & { onInput?: (v: string) => void },
  renderOptions?: { container?: HTMLElement },
) {
  const onInput = vi.fn()
  const suggestions = overrides?.suggestions ?? ['gpt-4o', 'gpt-4', 'claude-3']
  const Harness = () => {
    const [value, setValue] = createSignal('')
    return (
      <SuggestionField
        id="model"
        value={value()}
        onInput={(v) => {
          onInput(v)
          setValue(v)
        }}
        suggestions={suggestions}
        {...overrides}
      />
    )
  }
  const utils = render(() => <Harness />, renderOptions)
  return { ...utils, onInput }
}

function combobox() {
  return screen.getByRole<HTMLInputElement>('combobox')
}

function listbox() {
  // The list is intentionally hidden while closed; the ARIA test asserts the
  // hidden state, so the query has to see past it.
  return screen.getByRole('listbox', { hidden: true })
}

function options(): HTMLElement[] {
  return Array.from(listbox().querySelectorAll<HTMLElement>('[role="option"]'))
}

function openList() {
  // A real focus(): jsdom's fireEvent.focus does not move
  // document.activeElement, and the Escape/click contracts are asserted
  // against the real focus owner.
  combobox().focus()
}

describe('SuggestionField — the combobox the datalist was not (fix-kit-rowlist)', () => {
  it('is a combobox: input role, listbox, and the ARIA wiring a combobox needs', () => {
    harness()
    const input = combobox()
    expect(input.getAttribute('aria-expanded')).toBe('false')
    expect(input.getAttribute('aria-controls')).toBe('model-suggestions')
    expect(input.getAttribute('aria-autocomplete')).toBe('list')
    expect(input.getAttribute('aria-activedescendant')).toBeNull()
    const list = listbox()
    expect(list.id).toBe('model-suggestions')
    expect(list.hasAttribute('hidden')).toBe(true)
  })

  it('opens the list on focus and shows every suggestion when nothing is typed', () => {
    harness()
    openList()
    expect(combobox().getAttribute('aria-expanded')).toBe('true')
    expect(options().map((o) => o.textContent)).toEqual(['gpt-4o', 'gpt-4', 'claude-3'])
  })

  it('does not close on its own input — typing re-filters and keeps the list open', () => {
    const { onInput } = harness()
    openList()
    fireEvent.input(combobox(), { target: { value: 'g' } })
    expect(onInput).toHaveBeenCalledWith('g')
    // The list is still open and now offers only the prefix matches.
    expect(combobox().getAttribute('aria-expanded')).toBe('true')
    expect(options().map((o) => o.textContent)).toEqual(['gpt-4o', 'gpt-4'])
    fireEvent.input(combobox(), { target: { value: 'gpt' } })
    expect(combobox().getAttribute('aria-expanded')).toBe('true')
    expect(options().map((o) => o.textContent)).toEqual(['gpt-4o', 'gpt-4'])
  })

  it('stays free text: a value the list does not contain is typed and passed through', () => {
    const { onInput } = harness()
    openList()
    // "qwen3" is not in the offers — it is still typeable and still reported.
    fireEvent.input(combobox(), { target: { value: 'qwen3' } })
    expect(onInput).toHaveBeenCalledWith('qwen3')
    expect(combobox().value).toBe('qwen3')
    // With no prefix match the list hides rather than refusing the value.
    expect(combobox().getAttribute('aria-expanded')).toBe('false')
  })

  it('hides the list when a filter matches nothing, and it reopens as typing matches again', () => {
    harness()
    openList()
    fireEvent.input(combobox(), { target: { value: 'zzz' } })
    expect(combobox().getAttribute('aria-expanded')).toBe('false')
    fireEvent.input(combobox(), { target: { value: 'cl' } })
    expect(combobox().getAttribute('aria-expanded')).toBe('true')
    expect(options().map((o) => o.textContent)).toEqual(['claude-3'])
  })

  it('the first match follows the typed value, and Enter takes it', () => {
    const { onInput } = harness()
    openList()
    fireEvent.input(combobox(), { target: { value: 'gp' } })
    // The first match is active — Enter takes what the user is looking at.
    expect(combobox().getAttribute('aria-activedescendant')).toBe('model-suggestions-option-0')
    fireEvent.keyDown(combobox(), { key: 'Enter' })
    expect(onInput).toHaveBeenLastCalledWith('gpt-4o')
    expect(combobox().value).toBe('gpt-4o')
    expect(combobox().getAttribute('aria-expanded')).toBe('false')
  })

  it('ArrowDown moves the active option and Enter takes the moved-to one', () => {
    const { onInput } = harness()
    openList()
    fireEvent.keyDown(combobox(), { key: 'ArrowDown' }) // first
    fireEvent.keyDown(combobox(), { key: 'ArrowDown' }) // second
    expect(combobox().getAttribute('aria-activedescendant')).toBe('model-suggestions-option-1')
    expect(options()[1].getAttribute('aria-selected')).toBe('true')
    fireEvent.keyDown(combobox(), { key: 'Enter' })
    expect(onInput).toHaveBeenLastCalledWith('gpt-4')
  })

  it('ArrowUp opens at the last option, and the walk clamps at the ends', () => {
    harness()
    openList()
    fireEvent.keyDown(combobox(), { key: 'ArrowUp' })
    expect(combobox().getAttribute('aria-activedescendant')).toBe('model-suggestions-option-2')
    fireEvent.keyDown(combobox(), { key: 'ArrowUp' })
    expect(combobox().getAttribute('aria-activedescendant')).toBe('model-suggestions-option-1')
    // Clamp at the top, never wrap into the void.
    fireEvent.keyDown(combobox(), { key: 'ArrowUp' })
    fireEvent.keyDown(combobox(), { key: 'ArrowUp' })
    fireEvent.keyDown(combobox(), { key: 'ArrowUp' })
    expect(combobox().getAttribute('aria-activedescendant')).toBe('model-suggestions-option-0')
  })

  it('Escape dismisses the list without losing what was typed or where focus is', () => {
    const { onInput } = harness()
    openList()
    fireEvent.input(combobox(), { target: { value: 'gp' } })
    expect(combobox().getAttribute('aria-expanded')).toBe('true')
    fireEvent.keyDown(combobox(), { key: 'Escape' })
    expect(combobox().getAttribute('aria-expanded')).toBe('false')
    // What was typed stays, and the input still has focus.
    expect(combobox().value).toBe('gp')
    expect(document.activeElement).toBe(combobox())
    // Nothing was submitted on the way out.
    expect(onInput).toHaveBeenCalledTimes(1)
  })

  it('ArrowDown opens a closed list with the first option active', () => {
    harness()
    openList()
    fireEvent.keyDown(combobox(), { key: 'Escape' })
    expect(combobox().getAttribute('aria-expanded')).toBe('false')
    fireEvent.keyDown(combobox(), { key: 'ArrowDown' })
    expect(combobox().getAttribute('aria-expanded')).toBe('true')
    expect(combobox().getAttribute('aria-activedescendant')).toBe('model-suggestions-option-0')
  })

  it('a click takes the option and keeps focus in the input', () => {
    const { onInput } = harness()
    openList()
    const opt = screen.getByRole('option', { name: 'claude-3' })
    fireEvent.mouseDown(opt)
    fireEvent.click(opt)
    expect(onInput).toHaveBeenLastCalledWith('claude-3')
    expect(combobox().value).toBe('claude-3')
    expect(document.activeElement).toBe(combobox())
  })

  it('blur closes the list', () => {
    harness()
    openList()
    expect(combobox().getAttribute('aria-expanded')).toBe('true')
    fireEvent.blur(combobox())
    expect(combobox().getAttribute('aria-expanded')).toBe('false')
  })

  it('an empty suggestion set offers nothing, even on focus', () => {
    harness({ suggestions: [] })
    openList()
    expect(combobox().getAttribute('aria-expanded')).toBe('false')
    expect(combobox().getAttribute('aria-activedescendant')).toBeNull()
  })

  it('opens when suggestions arrive after focus — discovery is async by design', () => {
    const Harness = () => {
      const [value, setValue] = createSignal('')
      const [sugs, setSugs] = createSignal<string[]>([])
      return (
        <SuggestionField
          id="model"
          value={value()}
          onInput={setValue}
          suggestions={sugs()}
          onFocus={() => setSugs(['gpt-4o', 'qwen3'])}
        />
      )
    }
    render(() => <Harness />)
    combobox().focus()
    // onFocus fired the discovery; the moment the list lands it opens.
    expect(combobox().getAttribute('aria-expanded')).toBe('true')
    expect(options().map((o) => o.textContent)).toEqual(['gpt-4o', 'qwen3'])
  })

  it('composes Field like every kit form control: label, required marker, error', () => {
    harness({ label: 'Model id', required: true, error: 'Model name is required' })
    expect(screen.getByText('Model id')).toBeTruthy()
    expect(screen.getByText('Model name is required')).toBeTruthy()
    const input = combobox()
    expect(input.getAttribute('aria-invalid')).toBe('true')
    expect(input.getAttribute('aria-describedby')).toMatch(/model__error/)
    expect(input).toHaveProperty('required', true)
  })
})

/** A fake laid-out input rect — jsdom measures nothing. */
function rect(left: number, top: number, width: number): DOMRect {
  return {
    x: left,
    y: top,
    left,
    top,
    right: left + width,
    bottom: top + 24,
    width,
    height: 24,
    toJSON: () => ({}),
  }
}

describe('the floating list (nocx-0plm6) — portalled out of the clipping context', () => {
  it('is not a descendant of a clipped ancestor — an overflow:hidden container cannot cut it', () => {
    // The endpoint dialog clips its body the way this container does; the
    // list must live OUTSIDE the flow it used to be clipped by.
    const clipped = document.createElement('div')
    clipped.setAttribute('data-testid', 'clipped-ancestor')
    clipped.style.overflow = 'hidden'
    clipped.style.maxHeight = '60px'
    document.body.appendChild(clipped)
    try {
      harness({}, { container: clipped })
      openList()
      const list = listbox()
      // In-flow markup fails this: the list was a child of the clipped
      // container, and `closest` walked straight to it.
      expect(list.closest('[data-testid="clipped-ancestor"]')).toBeNull()
      expect(document.body.contains(list)).toBe(true)
    } finally {
      clipped.remove()
    }
  })

  it('positions against the input rect and matches its width', () => {
    harness()
    const input = combobox()
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 768 })
    vi.spyOn(input, 'getBoundingClientRect').mockReturnValue(rect(40, 100, 200))
    openList()
    const list = listbox()
    expect(list.style.left).toBe('40px')
    expect(list.style.top).toBe('128px') // input bottom (124) + the 4px gap
    expect(list.style.width).toBe('200px')
  })

  it('flips above the input when there is no room below', () => {
    harness()
    const input = combobox()
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 768 })
    vi.spyOn(input, 'getBoundingClientRect').mockReturnValue(rect(40, 700, 200))
    const list = listbox()
    Object.defineProperty(list, 'offsetHeight', { configurable: true, value: 120 })
    openList()
    // Room below is 768 − 724 = 44 < 124; the list goes ABOVE the input.
    expect(list.style.top).toBe('576px') // input top (700) − height (120) − gap (4)
    expect(list.style.left).toBe('40px')
  })

  it('closes when the container scrolls — a list that stopped anchoring to its input does not float', () => {
    const scroller = document.createElement('div')
    scroller.style.overflow = 'auto'
    scroller.style.maxHeight = '120px'
    document.body.appendChild(scroller)
    try {
      harness({}, { container: scroller })
      openList()
      expect(combobox().getAttribute('aria-expanded')).toBe('true')
      fireEvent.scroll(scroller)
      // Closed — and nothing is left floating over unrelated content: the
      // portalled list is hidden, not parked over the page.
      expect(combobox().getAttribute('aria-expanded')).toBe('false')
      expect(listbox().hasAttribute('hidden')).toBe(true)
    } finally {
      scroller.remove()
    }
  })

  it('scrolling INSIDE the list does not close it — a long list must stay scrollable', () => {
    const many = Array.from({ length: 60 }, (_, i) => `model-${i}`)
    harness({ suggestions: many })
    openList()
    fireEvent.scroll(listbox())
    expect(combobox().getAttribute('aria-expanded')).toBe('true')
  })

  it('an outside pointerdown dismisses the list', () => {
    harness()
    openList()
    fireEvent.pointerDown(document.body)
    expect(combobox().getAttribute('aria-expanded')).toBe('false')
  })

  it('a pointer click on an option lands — the dismissal contains the list, so the click is not eaten', () => {
    const { onInput } = harness()
    openList()
    const opt = screen.getByRole('option', { name: 'claude-3' })
    // pointerdown reaches the document-level dismissal FIRST. It must
    // contain the list: the pointer is on an option.
    fireEvent.pointerDown(opt)
    expect(combobox().getAttribute('aria-expanded')).toBe('true')
    fireEvent.mouseDown(opt)
    fireEvent.click(opt)
    expect(onInput).toHaveBeenLastCalledWith('claude-3')
    expect(combobox().value).toBe('claude-3')
    expect(combobox().getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(combobox())
  })

  it('the combobox ARIA resolves across the portal, and Escape keeps what was typed', () => {
    harness()
    openList()
    const input = combobox()
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    const controls = input.getAttribute('aria-controls')!
    const active = input.getAttribute('aria-activedescendant')!
    // Both ids resolve to elements that live in the portalled list.
    const list = document.getElementById(controls)
    expect(list).not.toBeNull()
    expect(list!.getAttribute('role')).toBe('listbox')
    expect(document.getElementById(active)).not.toBeNull()
    fireEvent.input(input, { target: { value: 'gp' } })
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(input.getAttribute('aria-expanded')).toBe('false')
    expect(input.value).toBe('gp')
    expect(document.activeElement).toBe(input)
  })

  it('unmounting with the list open leaves nothing behind in the portal host', () => {
    const utils = harness()
    openList()
    expect(document.body.querySelector('.ui-suggestion-field__list')).not.toBeNull()
    utils.unmount()
    expect(document.body.querySelector('.ui-suggestion-field__list')).toBeNull()
    expect(document.getElementById('model-suggestions')).toBeNull()
  })
})
