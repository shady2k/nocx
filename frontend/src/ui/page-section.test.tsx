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

  it('emits its identity and nothing else', () => {
    subject()
    expect(document.querySelector('section')?.getAttribute('class')).toBe('ui-page-section')
  })

  // One explanation for the section beats the same sentence repeated under
  // every row in it — which is what the Vault page did with "Vault is locked."
  describe('description', () => {
    it('states the description above the children when given one', () => {
      subject({ description: 'Unlock the vault to change how it is protected.' })
      const desc = document.querySelector('.ui-page-section__desc')
      expect(desc).not.toBeNull()
      expect(desc!.textContent).toBe('Unlock the vault to change how it is protected.')
      // Above the content, not inside it: it explains the whole section.
      expect(desc!.nextElementSibling?.classList.contains('ui-stack')).toBe(true)
    })

    it('renders no description element when there is none', () => {
      subject()
      expect(document.querySelector('.ui-page-section__desc')).toBeNull()
    })
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
