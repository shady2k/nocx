// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup } from '@solidjs/testing-library'
import { Toolbar, type ToolbarProps } from './toolbar'

afterEach(() => cleanup())

function subject(overrides?: Partial<ToolbarProps>) {
  const props: ToolbarProps = {
    children: 'Toolbar content',
    ...overrides,
  }
  return render(() => <Toolbar {...props} />)
}

describe('Toolbar', () => {
  it('renders children', () => {
    subject()
    expect(screen.getByText('Toolbar content')).toBeTruthy()
  })

  it('sets class on the wrapper', () => {
    subject({ class: 'cm-header' })
    // The wrapper is a div with role="toolbar"
    const el = screen.getByRole('toolbar')
    expect(el.getAttribute('class')).toBe('cm-header')
  })

  it('renders with toolbar role', () => {
    subject()
    expect(screen.getByRole('toolbar')).toBeTruthy()
  })

  it('sets aria-label when provided', () => {
    subject({ ariaLabel: 'Profile actions' })
    const toolbar = screen.getByRole('toolbar')
    expect(toolbar.getAttribute('aria-label')).toBe('Profile actions')
  })

  it('renders complex children', () => {
    subject({
      children: [<h1>Title</h1>, <button>Action</button>],
    })
    expect(screen.getByText('Title')).toBeTruthy()
    expect(screen.getByText('Action')).toBeTruthy()
  })
})
