// @vitest-environment jsdom
//
// The whole harness is a stub callback: no transport, no dispatcher, no
// backend. That is the point of the component — the question can be answered
// correctly long before anything exists to ask it.
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, cleanup } from '@solidjs/testing-library'
import { CollisionDialog, type CollisionRequest, type CollisionResult } from './collision-dialog'
import { clearStack, closeTopmost } from './overlay/stack'

afterEach(() => {
  cleanup()
  clearStack()
})

function subject(overrides?: Partial<CollisionRequest>) {
  const onResolve = vi.fn<(result: CollisionResult) => void>()
  const request: CollisionRequest = {
    name: 'report.pdf',
    destination: '/var/www/uploads',
    remaining: 4,
    ...overrides,
  }
  render(() => <CollisionDialog request={request} onResolve={onResolve} />)
  return { onResolve }
}

const button = (name: string) => screen.getByRole('button', { name })
const dialogEl = () => document.querySelector('dialog.nocx-dialog') as HTMLDialogElement

describe('CollisionDialog — the question', () => {
  it('names the file and where it is going', () => {
    subject()
    const body = document.querySelector('.nocx-dialog__body')!
    expect(body.textContent).toContain('report.pdf')
    expect(body.textContent).toContain('/var/www/uploads')
  })

  it('offers exactly the three answers', () => {
    subject()
    const labels = Array.from(
      document.querySelectorAll('.nocx-dialog__actions button'),
      (b) => b.textContent,
    )
    expect(labels).toEqual(['Overwrite', 'Keep both', 'Skip'])
  })

  it('marks overwrite as the destructive answer and focuses the safe one', () => {
    subject()
    expect(button('Overwrite').getAttribute('data-variant')).toBe('danger')
    expect(button('Skip').hasAttribute('autofocus')).toBe(true)
  })
})

describe('CollisionDialog — each answer reaches the callback', () => {
  it('overwrite', () => {
    const { onResolve } = subject()
    fireEvent.click(button('Overwrite'))
    expect(onResolve).toHaveBeenCalledWith({ answer: 'overwrite', applyToAll: false })
  })

  it('keep both', () => {
    const { onResolve } = subject()
    fireEvent.click(button('Keep both'))
    expect(onResolve).toHaveBeenCalledWith({ answer: 'keepBoth', applyToAll: false })
  })

  it('skip', () => {
    const { onResolve } = subject()
    fireEvent.click(button('Skip'))
    expect(onResolve).toHaveBeenCalledWith({ answer: 'skip', applyToAll: false })
  })
})

describe('CollisionDialog — the checkbox rides along with the answer', () => {
  it.each([
    ['Overwrite', 'overwrite'],
    ['Keep both', 'keepBoth'],
    ['Skip', 'skip'],
  ] as const)('reports applyToAll with %s', (label, answer) => {
    const { onResolve } = subject()
    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.click(button(label))
    expect(onResolve).toHaveBeenCalledWith({ answer, applyToAll: true })
  })

  it('starts unticked on every opening — the dialog stores no policy', () => {
    const first = subject()
    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.click(button('Overwrite'))
    expect(first.onResolve).toHaveBeenCalledWith({ answer: 'overwrite', applyToAll: true })

    cleanup()
    clearStack()

    const second = subject()
    expect(screen.getByRole('checkbox')).toHaveProperty('checked', false)
    fireEvent.click(button('Overwrite'))
    expect(second.onResolve).toHaveBeenCalledWith({ answer: 'overwrite', applyToAll: false })
  })

  it('names how many files the tick would reach', () => {
    subject({ remaining: 4 })
    expect(screen.getByText('Apply to the 3 remaining files')).toBeTruthy()
  })

  it('says "1 remaining file" rather than "1 remaining files"', () => {
    subject({ remaining: 2 })
    expect(screen.getByText('Apply to the 1 remaining file')).toBeTruthy()
  })
})

describe('CollisionDialog — a lone file is not asked about the others', () => {
  it('draws no checkbox when remaining is 1', () => {
    subject({ remaining: 1 })
    expect(screen.queryByRole('checkbox')).toBeNull()
  })

  it('still answers, with applyToAll false', () => {
    const { onResolve } = subject({ remaining: 1 })
    fireEvent.click(button('Overwrite'))
    expect(onResolve).toHaveBeenCalledWith({ answer: 'overwrite', applyToAll: false })
  })
})

// The one outcome this dialog exists to prevent is a file on a server destroyed
// by an accident. So every way OUT of the dialog that is not a deliberate press
// answers `skip` — never `overwrite`.
describe('CollisionDialog — dismissal is skip', () => {
  it('Escape, as the native cancel', () => {
    const { onResolve } = subject()
    fireEvent(dialogEl(), new Event('cancel', { bubbles: true }))
    expect(onResolve).toHaveBeenCalledWith({ answer: 'skip', applyToAll: false })
  })

  it('Escape, through the overlay stack', () => {
    const { onResolve } = subject()
    expect(closeTopmost()).toBe(true)
    expect(onResolve).toHaveBeenCalledWith({ answer: 'skip', applyToAll: false })
  })

  it('a click outside the panel', () => {
    const { onResolve } = subject()
    // jsdom reports a zero-sized panel, so any non-origin point is outside it.
    // The origin itself is excluded by Dialog: a keyboard-activated click
    // reports 0,0 and must never read as a dismiss.
    fireEvent.mouseDown(dialogEl(), { clientX: 500, clientY: 400 })
    expect(onResolve).toHaveBeenCalledWith({ answer: 'skip', applyToAll: false })
  })

  it('carries the tick along, so a dismissed batch stays skipped', () => {
    const { onResolve } = subject()
    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent(dialogEl(), new Event('cancel', { bubbles: true }))
    expect(onResolve).toHaveBeenCalledWith({ answer: 'skip', applyToAll: true })
  })
})
