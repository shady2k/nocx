// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup } from '@solidjs/testing-library'
import { Section, type SectionProps } from './section'

afterEach(() => cleanup())

function subject(overrides?: Partial<SectionProps>) {
  const props: SectionProps = {
    title: 'Terminal',
    children: 'Section body',
    ...overrides,
  }
  return render(() => <Section {...props} />)
}

describe('Section', () => {
  it('renders the title as a heading', () => {
    subject()
    const heading = screen.getByText('Terminal')
    expect(heading.tagName).toBe('H2')
  })

  it('renders children', () => {
    subject()
    expect(screen.getByText('Section body')).toBeTruthy()
  })

  it('renders complex children', () => {
    subject({
      children: [<div class="st-row">Row 1</div>, <div class="st-row">Row 2</div>],
    })
    expect(screen.getByText('Row 1')).toBeTruthy()
    expect(screen.getByText('Row 2')).toBeTruthy()
  })

  it('sets class on the wrapper', () => {
    subject({ class: 'st-section' })
    const container = document.querySelector('.st-section')
    expect(container).not.toBeNull()
  })

  it('uses section element', () => {
    subject({ id: 'terminal-settings' })
    const section = document.querySelector('#terminal-settings')
    expect(section?.tagName).toBe('SECTION')
  })

  it('sets id for deep linking', () => {
    subject({ id: 'appearance' })
    const section = document.querySelector('#appearance')
    expect(section).not.toBeNull()
  })
})
