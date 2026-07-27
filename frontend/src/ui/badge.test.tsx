// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup } from '@solidjs/testing-library'
import { Badge, type BadgeProps } from './badge'

afterEach(() => cleanup())

function subject(overrides?: Partial<BadgeProps>) {
  const props: BadgeProps = {
    children: 'Customized',
    ...overrides,
  }
  return render(() => <Badge {...props} />)
}

describe('Badge', () => {
  it('renders text content', () => {
    subject()
    expect(screen.getByText('Customized')).toBeTruthy()
  })

  it('renders with default variant and no extra class', () => {
    subject()
    const el = screen.getByText('Customized')
    expect(el.classList.contains('ui-badge-warning')).toBe(false)
  })

  it('applies warning variant class', () => {
    subject({ variant: 'warning' })
    const el = screen.getByText('Customized')
    expect(el.classList.contains('ui-badge-warning')).toBe(true)
  })

  it('applies danger variant class', () => {
    subject({ variant: 'danger' })
    const el = screen.getByText('Customized')
    expect(el.classList.contains('ui-badge-danger')).toBe(true)
  })

  it('applies info variant class', () => {
    subject({ variant: 'info' })
    const el = screen.getByText('Customized')
    expect(el.classList.contains('ui-badge-info')).toBe(true)
  })

  it('combines variant and custom class', () => {
    subject({ variant: 'warning', class: 'st-provenance' })
    const el = screen.getByText('Customized')
    expect(el.classList.contains('ui-badge-warning')).toBe(true)
    expect(el.classList.contains('st-provenance')).toBe(true)
  })

  it('is an inline element', () => {
    subject()
    const el = screen.getByText('Customized')
    expect(el.tagName).toBe('SPAN')
  })
})
