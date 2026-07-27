// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup } from '@solidjs/testing-library'
import { PageHeader, type PageHeaderProps } from './page-header'

afterEach(() => cleanup())

function subject(overrides?: Partial<PageHeaderProps>) {
  const props: PageHeaderProps = {
    title: 'Settings',
    ...overrides,
  }
  return render(() => <PageHeader {...props} />)
}

describe('PageHeader', () => {
  it('renders the title as an h1', () => {
    subject()
    const heading = screen.getByText('Settings')
    expect(heading.tagName).toBe('H1')
  })

  it('renders the description when provided', () => {
    subject({ description: 'Configure your terminal' })
    expect(screen.getByText('Configure your terminal')).toBeTruthy()
  })

  it('does not render description when omitted', () => {
    subject()
    expect(screen.queryByText('Configure')).toBeNull()
  })

  it('renders actions when provided', () => {
    subject({ actions: <button>Save</button> })
    expect(screen.getByText('Save')).toBeTruthy()
  })

  it('does not render actions container when omitted', () => {
    const { container } = subject()
    expect(container.querySelector('.ui-page__header-actions')).toBeNull()
  })

  it('applies .ui-page__header class', () => {
    const { container } = subject()
    expect(container.querySelector('.ui-page__header')).not.toBeNull()
  })
})
