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

  // The class is the component's alone: a caller cannot add to it and cannot replace
  // it. `ui-section` is what section.css keys on, so a Section carrying anything else
  // would be a Section somebody else can restyle.
  it('emits its identity and nothing else', () => {
    subject()
    expect(document.querySelector('section')?.getAttribute('class')).toBe('ui-section')
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

  describe('divided', () => {
    it('forwards divided prop to inner Stack', () => {
      subject({ divided: true })
      const stack = document.querySelector('.ui-stack')
      expect(stack?.getAttribute('data-divided')).toBe('true')
    })

    it('omits data-divided when divided is not set', () => {
      subject()
      const stack = document.querySelector('.ui-stack')
      expect(stack?.hasAttribute('data-divided')).toBe(false)
    })
  })
})
