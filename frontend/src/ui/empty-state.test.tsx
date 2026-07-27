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

  it('sets combined class', () => {
    subject({ class: 'cm-list-empty' })
    const el = document.querySelector('.ui-empty-state')
    expect(el?.classList.contains('cm-list-empty')).toBe(true)
  })
})
