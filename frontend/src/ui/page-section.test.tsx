// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup } from '@solidjs/testing-library'
import { PageSection, type PageSectionProps } from './page-section'

afterEach(() => cleanup())

function subject(overrides?: Partial<PageSectionProps>) {
  const props: PageSectionProps = {
    title: 'Interface',
    children: 'Section content',
    ...overrides,
  }
  return render(() => <PageSection {...props} />)
}

describe('PageSection', () => {
  it('renders the title as an h2', () => {
    subject()
    const heading = screen.getByText('Interface')
    expect(heading.tagName).toBe('H2')
  })

  it('renders children', () => {
    subject()
    expect(screen.getByText('Section content')).toBeTruthy()
  })

  it('renders an id attribute when provided', () => {
    subject({ id: 'interface-section' })
    const section = document.getElementById('interface-section')
    expect(section).not.toBeNull()
    expect(section?.tagName).toBe('SECTION')
  })

  it('does not set id when omitted', () => {
    subject()
    const sections = document.querySelectorAll('section')
    expect(sections.length).toBe(1)
    expect(sections[0].hasAttribute('id')).toBe(false)
  })

  it('uses a <section> element', () => {
    subject()
    const section = document.querySelector('section')
    expect(section).not.toBeNull()
  })

  it('sets class on the wrapper', () => {
    subject({ class: 'my-section' })
    const section = document.querySelector('section.my-section')
    expect(section).not.toBeNull()
  })
})
