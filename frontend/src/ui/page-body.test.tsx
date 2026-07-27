// @vitest-environment jsdom
import { describe, expect, it, afterEach } from 'vitest'
import { render, cleanup } from '@solidjs/testing-library'
import { PageBody, type PageBodyProps } from './page-body'

afterEach(() => cleanup())

function subject(overrides?: Partial<PageBodyProps>) {
  const props: PageBodyProps = {
    children: 'Body content',
    ...overrides,
  }
  return render(() => <PageBody {...props} />)
}

describe('PageBody', () => {
  it('renders children', () => {
    subject()
    const el = document.querySelector('.ui-page__body')
    expect(el?.textContent).toBe('Body content')
  })

  it('applies .ui-page__body class', () => {
    subject()
    expect(document.querySelector('.ui-page__body')).not.toBeNull()
  })
})
