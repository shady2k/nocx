// @vitest-environment jsdom
/**
 * i18n tests for the API workbench surface (nocx-dej6.3.8) — the proof that
 * its call sites read through the catalogue rather than the literal: the
 * workbench mounted with no collections open under 'ru' shows the Russian
 * catalogue values, where the hardcoded checker can only prove that the
 * literal sits inside a t() call. The same distinction connections.i18n.test
 * draws for its surface.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render } from '@solidjs/testing-library'
import { ApiPane } from './api-pane'
import { createApiStore } from './api-store'
import { servicesFixture, noCollections } from './api-test-fixtures'
import { setActiveLocale } from '../i18n/locale-model'

/** The workbench as the user starts it: a store over a backend holding no
 *  collections, mounted directly so the test reads exactly what this surface
 *  renders. */
function mountEmpty() {
  const store = createApiStore(servicesFixture(noCollections()))
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <ApiPane store={store} />, { container })
  return container
}

// The locale signal is module state; every test starts and leaves it English
// so one test's switch cannot decide what the next test reads.
beforeEach(() => {
  setActiveLocale('en')
})

afterEach(() => {
  setActiveLocale('en')
  vi.clearAllMocks()
  document.body.innerHTML = ''
})

describe('api workbench surface renders through the catalogue', () => {
  it('shows the Russian empty-state title under ru', () => {
    setActiveLocale('ru')
    const container = mountEmpty()
    expect(container.textContent).toContain('Коллекции не открыты')
    expect(container.textContent).not.toContain('No collections open')
  })

  it('shows a Russian aria-label under ru', () => {
    setActiveLocale('ru')
    const container = mountEmpty()
    // The filter lives on the search field's aria-label, not in textContent
    // — a placeholder is not rendered text.
    const filter = container.querySelector<HTMLInputElement>('#api-filter')
    expect(filter?.getAttribute('aria-label')).toBe('Фильтр коллекций и запросов')
  })

  it('shows the English catalogue values under en', () => {
    const container = mountEmpty()
    expect(container.textContent).toContain('No collections open')
    const filter = container.querySelector<HTMLInputElement>('#api-filter')
    expect(filter?.getAttribute('aria-label')).toBe('Filter collections and requests')
  })
})
