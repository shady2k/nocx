// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@solidjs/testing-library'
import { StatusCard, type StatusCardProps } from './status-card'
import { Button } from './button'

afterEach(() => cleanup())

function subject(overrides?: Partial<StatusCardProps>) {
  const props: StatusCardProps = {
    title: 'Vault is locked',
    ...overrides,
  }
  return render(() => <StatusCard {...props} />)
}

describe('StatusCard', () => {
  it('carries its identity class', () => {
    subject()
    expect(document.querySelector('.ui-status-card')).not.toBeNull()
  })

  it('states the title', () => {
    subject()
    expect(screen.getByText('Vault is locked')).toBeTruthy()
  })

  it('defaults to the neutral tone', () => {
    subject()
    expect(document.querySelector('.ui-status-card')!.getAttribute('data-tone')).toBe('neutral')
  })

  it('carries the tone it is given', () => {
    subject({ tone: 'warning' })
    expect(document.querySelector('.ui-status-card')!.getAttribute('data-tone')).toBe('warning')
  })

  it('omits the description element when there is no description', () => {
    subject()
    expect(document.querySelector('.ui-status-card__desc')).toBeNull()
  })

  it('states the description when given one', () => {
    subject({ description: 'Unlock to use saved passwords.' })
    expect(screen.getByText('Unlock to use saved passwords.')).toBeTruthy()
  })

  it('omits the icon slot entirely when no icon is given, so nothing indents around empty space', () => {
    subject()
    expect(document.querySelector('.ui-status-card__icon')).toBeNull()
  })

  it('renders the icon it is given', () => {
    subject({ icon: <svg data-testid="glyph" /> })
    expect(document.querySelector('.ui-status-card__icon svg')).not.toBeNull()
  })

  // The card exists to pair a state with the one thing to do about it. A card
  // whose action does not reach its handler announces a problem and offers a
  // remedy that does nothing — worse than announcing nothing.
  it('the action it renders is clickable and reaches its handler', () => {
    let clicked = 0
    subject({
      action: (
        <Button variant="primary" onClick={() => (clicked += 1)}>
          Unlock
        </Button>
      ),
    })
    const button = screen.getByRole('button', { name: 'Unlock' })
    expect((button as HTMLButtonElement).disabled).toBe(false)
    fireEvent.click(button)
    expect(clicked).toBe(1)
  })
})
