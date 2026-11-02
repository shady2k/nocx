// @vitest-environment jsdom
/**
 * i18n tests for the connections surface (nocx-dej6.3.2) — the proof that a
 * call-site reads its strings through the catalogue rather than the literal:
 * the empty list mounted under 'en' shows the English catalogue values, and
 * the same mount under 'ru' shows Russian. The catalogue parity fixture only
 * checks that en and ru know the same keys; these tests check that a surface
 * actually resolves them at render time.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render } from '@solidjs/testing-library'
import { ConnectionsView } from './connections'
import { ProfileClient } from './profiles'
import type { VaultClient } from './vault-client'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { setActiveLocale } from './i18n/locale-model'

function mountEmpty() {
  const pc = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))

  vi.spyOn(pc, 'listProfiles').mockResolvedValue([])
  vi.spyOn(pc, 'listGroups').mockResolvedValue([])
  vi.spyOn(pc, 'sessionStatus').mockResolvedValue({ statuses: {} })
  vi.spyOn(pc, 'loadEffective').mockResolvedValue({ profiles: [] })
  vi.spyOn(pc, 'connectionTest').mockResolvedValue({ outcome: 'accepted' })

  const vaultClient = {
    inventory: () => ({ entries: [] }),
  } as unknown as VaultClient
  const secretSource = {
    status: () => Promise.resolve({ state: 'unsealed' as const }),
    list: () => Promise.resolve([]),
    requestUnseal: () => Promise.resolve(),
    requestSetup: () => Promise.resolve(true),
    requestCreate: () => Promise.resolve(undefined),
  }
  const container = document.body.appendChild(document.createElement('div'))
  render(
    () => <ConnectionsView client={pc} vaultClient={vaultClient} secretSource={secretSource} />,
    { container },
  )
  return { container, client: pc }
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

describe('connections surface renders through the catalogue', () => {
  it('shows the English catalogue values under en', () => {
    const { container } = mountEmpty()
    // The empty list is the initial state — profiles() starts as [] — so the
    // empty state is on screen without waiting for loadAll.
    expect(container.textContent).toContain('No connections yet')
    expect(container.textContent).toContain('+ New connection')
  })

  it('shows Russian under ru', () => {
    setActiveLocale('ru')
    const { container } = mountEmpty()
    expect(container.textContent).not.toContain('No connections yet')
    expect(container.textContent).toContain('Нет подключений')
    // The filter label lives on the search input's aria-label, not in
    // textContent — a placeholder is not rendered text.
    const searchInput = container.querySelector<HTMLInputElement>('.ui-search-field__input')
    expect(searchInput?.getAttribute('aria-label')).toBe('Фильтр подключений')
  })
})
