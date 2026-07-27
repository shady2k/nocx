// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, cleanup } from '@solidjs/testing-library'
import { Page, type PageProps } from './page'
import { PageRail } from './page-rail'
import { PageSection } from './page-section'

afterEach(() => cleanup())

function subject(overrides?: Partial<PageProps>) {
  const props: PageProps = {
    title: 'Settings',
    children: 'Page body',
    ...overrides,
  }
  return render(() => <Page {...props} />)
}

describe('Page', () => {
  it('renders the title', () => {
    subject()
    expect(screen.getByText('Settings')).toBeTruthy()
  })

  it('renders the description when provided', () => {
    subject({ description: 'Configure your terminal' })
    expect(screen.getByText('Configure your terminal')).toBeTruthy()
  })

  it('renders children inside the scroller', () => {
    subject()
    expect(screen.getByText('Page body')).toBeTruthy()
  })

  it('renders actions when provided', () => {
    const onClick = () => {}
    subject({ actions: <button onClick={onClick}>Save</button> })
    expect(screen.getByText('Save')).toBeTruthy()
  })

  it('renders a leading rail when provided', () => {
    const { container } = render(() => (
      <Page title="Settings" leading={<nav>Navigation</nav>}>
        Content
      </Page>
    ))
    const rail = container.querySelector('.ui-page__rail')
    expect(rail).not.toBeNull()
    expect(rail!.textContent).toBe('Navigation')
  })

  it('renders PageRail inside the leading slot', () => {
    const { container } = render(() => (
      <Page
        title="Settings"
        leading={
          <PageRail>
            <nav>Nav</nav>
          </PageRail>
        }
      >
        Content
      </Page>
    ))
    // Page wraps leading content in .ui-page__rail; PageRail as leading
    // means a .ui-page__rail containing a .ui-page__rail — acceptable
    // but the outer one is the Page-managed container
    const rail = container.querySelector('.ui-page__rail')
    expect(rail).not.toBeNull()
  })

  it('applies .ui-page class to the root', () => {
    const { container } = subject()
    const root = container.querySelector('.ui-page')
    expect(root).not.toBeNull()
  })

  it('renders the header with title', () => {
    const { container } = subject()
    const header = container.querySelector('.ui-page__header')
    expect(header).not.toBeNull()
    expect(header!.textContent).toContain('Settings')
  })

  it('renders the body container', () => {
    const { container } = subject()
    expect(container.querySelector('.ui-page__body')).not.toBeNull()
  })

  it('renders the scroller', () => {
    const { container } = subject()
    expect(container.querySelector('.ui-page__scroll')).not.toBeNull()
  })

  it('works with PageSection children', () => {
    const { container } = render(() => (
      <Page title="Settings">
        <PageSection id="interface" title="Interface">
          Interface content
        </PageSection>
        <PageSection id="appearance" title="Appearance">
          Appearance content
        </PageSection>
      </Page>
    ))
    expect(screen.getByText('Interface')).toBeTruthy()
    expect(screen.getByText('Appearance')).toBeTruthy()
    expect(screen.getByText('Interface content')).toBeTruthy()
    expect(screen.getByText('Appearance content')).toBeTruthy()
    expect(container.querySelector('#interface')).not.toBeNull()
    expect(container.querySelector('#appearance')).not.toBeNull()
  })
})
