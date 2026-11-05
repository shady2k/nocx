// @vitest-environment jsdom
/**
 * i18n test for the skills surface (nocx-dej6.3f) — the proof that the
 * skills section reads its strings through the catalogue rather than the
 * literal: the page mounted under 'ru' shows Russian, both in the section
 * title and in a row's aria-label. The catalogue parity fixture only checks
 * that en and ru know the same keys; this test checks that the surface
 * actually resolves them at render time.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { cleanup, render, waitFor } from '@solidjs/testing-library'
import { SkillsSection } from './skills-section'
import { SkillsStore, type SkillsClientLike } from './skills-store'
import type { SkillsList } from './generated/skills.list'
import { setActiveLocale } from './i18n/locale-model'

// One authored, approved skill — enough for the list to render a row with
// its action group, which is where the aria-label lives.
const ONE_SKILL: SkillsList = {
  refused: [],
  documentPath: '/tmp/nocx/skills.json',
  skills: [
    {
      name: 'weather',
      description: 'How to read the weather',
      provenance: 'authored',
      path: '/tmp/nocx/skills/weather/SKILL.md',
      enabled: true,
      status: 'approved',
      usage: { count: 0 },
      pins: {},
    },
  ],
}

function fakeClient(overrides: Partial<SkillsClientLike> = {}): SkillsClientLike {
  return {
    list: vi.fn().mockResolvedValue(ONE_SKILL),
    setEnabled: vi.fn().mockResolvedValue({ name: 'weather', enabled: true }),
    setPin: vi.fn().mockResolvedValue({ name: 'weather', pins: {} }),
    remove: vi.fn().mockResolvedValue({ name: 'weather' }),
    approve: vi.fn().mockResolvedValue({ name: 'weather', status: 'approved' }),
    file: vi.fn(),
    files: vi.fn(),
    scan: vi.fn(),
    audit: vi.fn(),
    check: vi.fn(),
    ...overrides,
  }
}

// The locale signal is module state; every test starts and leaves it English
// so one test's switch cannot decide what the next test reads.
beforeEach(() => setActiveLocale('en'))
afterEach(() => {
  setActiveLocale('en')
  cleanup()
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

describe('skills section renders through the catalogue', () => {
  it('shows the Russian empty state under ru', async () => {
    setActiveLocale('ru')
    // The empty list is the state the acceptance names: no skills discovered,
    // so the section heading and the empty state stand on screen together.
    const store = new SkillsStore(
      fakeClient({
        list: vi
          .fn()
          .mockResolvedValue({ skills: [], documentPath: '/tmp/nocx/skills.json', refused: [] }),
      }),
    )
    const { container } = render(() => <SkillsSection store={store} />)

    await waitFor(() => expect(container.textContent).toContain('Найденные навыки'))
    expect(container.textContent).toContain('Навыки не найдены')
  })

  it('shows a Russian aria-label under ru on a row control', async () => {
    setActiveLocale('ru')
    // The store is built OUTSIDE the component expression on purpose: `props`
    // is a getter, so a `new SkillsStore(...)` written inside the JSX is
    // evaluated afresh on every access — the subscription and the refresh
    // would land on two different stores and the page would never leave
    // "Loading skills".
    const store = new SkillsStore(fakeClient())
    const { container } = render(() => <SkillsSection store={store} />)

    // The section heading is the visible proof the catalogue answered.
    await waitFor(() => expect(container.textContent).toContain('Найденные навыки'))
    // And the row's Open control names its row in Russian — an aria-label is
    // attribute copy, not textContent.
    const labels = Array.from(container.querySelectorAll('button')).map(
      (b) => b.getAttribute('aria-label') ?? '',
    )
    expect(labels.some((l) => l.includes('Открыть'))).toBe(true)
  })
})
