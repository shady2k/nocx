// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup } from '@solidjs/testing-library'
import { EmptyState, type EmptyStateProps } from './empty-state'

afterEach(() => cleanup())

function subject(overrides?: Partial<EmptyStateProps>) {
  const props: EmptyStateProps = {
    title: 'No connections yet',
    ...overrides,
  }
  return render(() => <EmptyState {...props} />)
}

describe('EmptyState', () => {
  it('renders the title', () => {
    subject()
    expect(screen.getByText('No connections yet')).toBeTruthy()
  })

  it('renders description when provided', () => {
    subject({ description: 'Click "+ New connection" to add one.' })
    expect(screen.getByText('Click "+ New connection" to add one.')).toBeTruthy()
  })

  it('does not render description when omitted', () => {
    subject()
    expect(document.querySelector('.ui-empty-state__desc')).toBeNull()
  })

  it('renders action when provided', () => {
    subject({ action: <button>New connection</button> })
    expect(screen.getByText('New connection')).toBeTruthy()
  })

  it('does not render action container when no action', () => {
    subject()
    expect(document.querySelector('.ui-empty-state__action')).toBeNull()
  })

  // A wall of text in the middle of an empty page reads as something that
  // failed to load. A glyph above it says the emptiness is the message.
  it('renders the icon it is given, above the title', () => {
    subject({ icon: <svg /> })
    const icon = document.querySelector('.ui-empty-state__icon')
    expect(icon).not.toBeNull()
    expect(icon!.querySelector('svg')).not.toBeNull()
    expect(icon!.nextElementSibling?.classList.contains('ui-empty-state__title')).toBe(true)
  })

  it('does not render an icon slot when there is no icon', () => {
    subject()
    expect(document.querySelector('.ui-empty-state__icon')).toBeNull()
  })

  // Identity, not passthrough. The old case asserted that a caller's class was
  // merged in; the `class` prop is gone (§3.6), so what matters now is that the
  // element names itself and nothing else — a stray class here would mean some
  // caller had found a way back in.
  it('emits its identity and nothing else', () => {
    subject()
    const el = document.querySelector('.ui-empty-state')
    expect(el).not.toBeNull()
    expect(el!.getAttribute('class')).toBe('ui-empty-state')
  })
})
