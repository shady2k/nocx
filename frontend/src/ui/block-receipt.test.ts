// @vitest-environment jsdom
//
// BlockReceipt (ui/block-receipt.ts) — the after-submit capture receipt.
// The kit contract, pinned: it appears non-modal (never steals focus), one
// row per capture with the kind badge, the masked value and an editable
// name, one primary action naming its scope, per-row drop, hover reported
// so the host can emphasise the block's chip, and the capture window
// retiring the receipt with an honest line and no actions that could fail.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { BlockReceipt, type BlockReceiptCapture } from './block-receipt'

function capture(overrides: Partial<BlockReceiptCapture> = {}): BlockReceiptCapture {
  return {
    captureId: 'cap_1',
    kindLabel: 'OpenAI key',
    maskedValue: 'sk-p...7890',
    suggestedName: 'openrouter.ai',
    ...overrides,
  }
}

function mount(
  captures: BlockReceiptCapture[],
  callbacks: Partial<Parameters<typeof makeReceipt>[1]> = {},
): {
  receipt: BlockReceipt
  container: HTMLElement
  calls: {
    onSaveAll: ReturnType<typeof vi.fn>
    onDismiss: ReturnType<typeof vi.fn>
    onHover: ReturnType<typeof vi.fn>
    onExitReview: ReturnType<typeof vi.fn>
  }
} {
  return makeReceipt(captures, callbacks)
}

function makeReceipt(
  captures: BlockReceiptCapture[],
  callbacks: Partial<{
    onSaveAll: (rows: ReadonlyArray<{ captureId: string; name: string }>) => void
    onDismiss: (captureId: string) => void
    onHover: (captureId: string | null) => void
    onExitReview: () => void
  }>,
): {
  receipt: BlockReceipt
  container: HTMLElement
  calls: {
    onSaveAll: ReturnType<typeof vi.fn>
    onDismiss: ReturnType<typeof vi.fn>
    onHover: ReturnType<typeof vi.fn>
    onExitReview: ReturnType<typeof vi.fn>
  }
} {
  const calls = {
    onSaveAll: vi.fn(),
    onDismiss: vi.fn(),
    onHover: vi.fn(),
    onExitReview: vi.fn(),
  }
  const receipt = new BlockReceipt(captures, {
    onSaveAll: callbacks.onSaveAll ?? calls.onSaveAll,
    onDismiss: callbacks.onDismiss ?? calls.onDismiss,
    onHover: callbacks.onHover ?? calls.onHover,
    onExitReview: callbacks.onExitReview ?? calls.onExitReview,
  })
  const container = document.createElement('div')
  container.className = 'cmd-block'
  document.body.appendChild(container)
  receipt.mount(container)
  return { receipt, container, calls }
}

beforeEach(() => {
  vi.useRealTimers()
})

describe('BlockReceipt: the kit contract', () => {
  it('renders one row per capture with the kind badge, masked value and pre-filled name', () => {
    const { container } = mount([
      capture({
        captureId: 'cap_1',
        kindLabel: 'OpenAI key',
        maskedValue: 'sk-p...7890',
        suggestedName: 'openrouter.ai',
      }),
      capture({
        captureId: 'cap_2',
        kindLabel: 'GitHub token',
        maskedValue: 'ghp...abcd',
        suggestedName: 'github-pat',
      }),
    ])
    const rows = container.querySelectorAll('.ui-block-receipt__row')
    expect(rows.length).toBe(2)
    const first = rows[0]
    expect(first.querySelector('.ui-badge')?.textContent).toBe('OpenAI key')
    expect(first.querySelector('.ui-block-receipt__value')?.textContent).toBe('sk-p...7890')
    const input = first.querySelector<HTMLInputElement>('.ui-text-field__input')
    expect(input?.value).toBe('openrouter.ai')
  })

  it('the primary action names its scope: Save for one row, Save 2 for two', () => {
    const one = mount([capture()])
    expect(one.container.querySelector('.ui-block-receipt__primary')?.textContent).toBe('Save')
    const two = mount([capture(), capture({ captureId: 'cap_2' })])
    expect(two.container.querySelector('.ui-block-receipt__primary')?.textContent).toBe('Save 2')
  })

  it('showing never steals focus: no element is focused on mount', () => {
    mount([capture()])
    expect(document.activeElement).not.toBe(document.querySelector('.ui-text-field__input'))
  })

  it('the primary action hands over every row still in play with the CURRENT names', () => {
    const { container, calls } = mount([capture(), capture({ captureId: 'cap_2' })])
    const inputs = container.querySelectorAll<HTMLInputElement>('.ui-text-field__input')
    inputs[0].value = 'edited-name'
    container.querySelector<HTMLButtonElement>('.ui-block-receipt__primary')!.click()
    expect(calls.onSaveAll).toHaveBeenCalledWith([
      { captureId: 'cap_1', name: 'edited-name' },
      { captureId: 'cap_2', name: 'openrouter.ai' },
    ])
  })

  it('a clean save removes exactly that row and relabels the primary action', () => {
    const { receipt, container } = mount([capture(), capture({ captureId: 'cap_2' })])
    receipt.removeRow('cap_1')
    expect(container.querySelectorAll('.ui-block-receipt__row').length).toBe(1)
    expect(container.querySelector('.ui-block-receipt__primary')?.textContent).toBe('Save')
  })

  it('a failed save reports on the row and keeps it', () => {
    const { receipt, container } = mount([capture()])
    receipt.markFailed('cap_1', 'could not save — try again')
    expect(container.querySelector('.ui-block-receipt__row-error')?.textContent).toContain(
      'could not save',
    )
    expect(container.querySelectorAll('.ui-block-receipt__row').length).toBe(1)
  })

  it('the drop control reports the capture id', () => {
    const { container, calls } = mount([capture(), capture({ captureId: 'cap_2' })])
    container.querySelectorAll<HTMLButtonElement>('.ui-block-receipt__drop')[1].click()
    expect(calls.onDismiss).toHaveBeenCalledWith('cap_2')
  })

  it('hover reports the row under the pointer, and leaving reports null', () => {
    const { container, calls } = mount([capture(), capture({ captureId: 'cap_2' })])
    const rows = container.querySelectorAll<HTMLElement>('.ui-block-receipt__row')
    rows[0].dispatchEvent(new MouseEvent('mouseenter', { bubbles: true }))
    expect(calls.onHover).toHaveBeenLastCalledWith('cap_1')
    rows[0].dispatchEvent(new MouseEvent('mouseleave', { bubbles: true }))
    expect(calls.onHover).toHaveBeenLastCalledWith(null)
  })

  it('⌘S inside the receipt performs the primary action; ⇧⌘S does too', () => {
    const { container, calls } = mount([capture()])
    const input = container.querySelector<HTMLInputElement>('.ui-text-field__input')!
    input.dispatchEvent(
      new KeyboardEvent('keydown', { key: 's', metaKey: true, bubbles: true, cancelable: true }),
    )
    expect(calls.onSaveAll).toHaveBeenCalledTimes(1)
  })

  it('Escape inside the receipt returns focus to the editor', () => {
    const { container, calls } = mount([capture()])
    const input = container.querySelector<HTMLInputElement>('.ui-text-field__input')!
    input.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )
    expect(calls.onExitReview).toHaveBeenCalledTimes(1)
  })

  it('⇧⌘S review mode focuses the first name field', () => {
    const { receipt, container } = mount([capture(), capture({ captureId: 'cap_2' })])
    receipt.enterReview()
    expect(document.activeElement).toBe(container.querySelector('.ui-text-field__input'))
  })

  // The offer used to retire itself on a timer, and in front of a user it
  // expired while they were still reading the output the command had just
  // produced. There is no timer now: the receipt waits for an answer.
  it('does not retire itself over time — the offer waits for an answer', () => {
    vi.useFakeTimers()
    const { receipt, container, calls } = mount([capture()])
    vi.advanceTimersByTime(10 * 60_000)
    expect(container.querySelector('.ui-block-receipt__row')).not.toBeNull()
    receipt.saveAll()
    expect(calls.onSaveAll).toHaveBeenCalledTimes(1)
    vi.useRealTimers()
  })

  // The host keeps its receipts in a map keyed by block; a receipt that has
  // destroyed itself must say so, or it stays in that map and keeps being
  // handed the save chord.
  it('reports emptiness when its last row goes, and not before', () => {
    const { receipt } = mount([capture(), capture({ captureId: 'cap_2' })])
    expect(receipt.removeRow('cap_1')).toBe(false)
    expect(receipt.removeRow('cap_2')).toBe(true)
  })
})

describe('forConnection variant (nocx-pu4.7)', () => {
  it('renders a single-row receipt with SSH host kind', () => {
    const onSave = vi.fn()
    const onDismiss = vi.fn()
    const receipt = BlockReceipt.forConnection('pi@host-2', 'host-2', {
      onSave,
      onDismiss,
    })

    const root = receipt.root
    expect(root.className).toContain('ui-block-receipt')

    // Kind badge says "SSH host"
    const kind = root.querySelector<HTMLElement>('.ui-block-receipt__kind')
    expect(kind?.textContent).toBe('SSH host')

    // Masked value is the destination
    const value = root.querySelector<HTMLElement>('.ui-block-receipt__value')
    expect(value?.textContent).toBe('pi@host-2')

    // Name field is pre-filled
    const input = root.querySelector<HTMLInputElement>('.ui-text-field__input')
    expect(input?.value).toBe('host-2')

    // One primary action
    const primary = root.querySelector<HTMLButtonElement>('.ui-block-receipt__primary')
    expect(primary?.textContent).toBe('Save')

    // Clicking Save calls onSave with the input value
    primary?.click()
    expect(onSave).toHaveBeenCalledWith('host-2')

    // Clicking Dismiss calls onDismiss
    const dismiss = root.querySelector<HTMLButtonElement>('.ui-block-receipt__drop')
    expect(dismiss?.textContent).toBe('Dismiss')
    dismiss?.click()
    expect(onDismiss).toHaveBeenCalled()
  })

  it('does not steal focus when mounted', () => {
    const container = document.createElement('div')
    const input = document.createElement('input')
    container.appendChild(input)
    document.body.appendChild(container)
    input.focus()

    const receipt = BlockReceipt.forConnection('host', 'host', {
      onSave: () => {},
      onDismiss: () => {},
    })
    receipt.mount(container)

    // Focus stays where it was — not stolen by the receipt.
    expect(document.activeElement).toBe(input)

    receipt.destroy()
    container.remove()
  })
})
