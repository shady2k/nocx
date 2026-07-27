// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { PageRail, type PageRailProps } from './page-rail'

afterEach(() => cleanup())

function subject(overrides?: Partial<PageRailProps>) {
  const props: PageRailProps = {
    children: 'Rail content',
    ...overrides,
  }
  return render(() => <PageRail {...props} />)
}

describe('PageRail', () => {
  it('renders children', () => {
    subject()
    const el = document.querySelector('.ui-page__rail')
    expect(el?.textContent).toBe('Rail content')
  })

  it('applies .ui-page__rail class', () => {
    subject()
    expect(document.querySelector('.ui-page__rail')).not.toBeNull()
  })
})
