// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup } from '@solidjs/testing-library'
import { SidebarView, type SidebarViewProps } from './sidebar-view'

afterEach(() => cleanup())

function subject(overrides?: Partial<SidebarViewProps>) {
  const props: SidebarViewProps = {
    title: 'Explorer',
    children: 'View content',
    ...overrides,
  }
  return render(() => <SidebarView {...props} />)
}

describe('SidebarView', () => {
  it('renders the title as an h2', () => {
    subject()
    const heading = screen.getByText('Explorer')
    expect(heading.tagName).toBe('H2')
  })

  it('renders children in the body', () => {
    subject()
    const body = document.querySelector('.ui-sidebar-view__body')
    expect(body?.textContent).toBe('View content')
  })

  it('renders actions when provided', () => {
    subject({ actions: <button>Collapse</button> })
    expect(screen.getByText('Collapse')).toBeTruthy()
  })

  it('renders filter when provided', () => {
    subject({ filter: <input placeholder="Filter" /> })
    const filter = document.querySelector('.ui-sidebar-view__filter')
    expect(filter).not.toBeNull()
  })

  it('does not render filter row when omitted', () => {
    subject()
    expect(document.querySelector('.ui-sidebar-view__filter')).toBeNull()
  })

  it('applies .ui-sidebar-view class to the root', () => {
    subject()
    expect(document.querySelector('.ui-sidebar-view')).not.toBeNull()
  })

  it('renders header', () => {
    subject()
    expect(document.querySelector('.ui-sidebar-view__header')).not.toBeNull()
  })
})
