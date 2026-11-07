// @vitest-environment jsdom
/**
 * i18n tests for the git surface (nocx-dej6 stage 3e) — the proof that the
 * panel reads its strings through the catalogue rather than the literal:
 * the empty panel mounted under 'en' shows the English catalogue values, and
 * the same mount under 'ru' shows Russian. The catalogue parity fixture only
 * checks that en and ru know the same keys; these tests check that the git
 * surface actually resolves them at render time.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render } from '@solidjs/testing-library'
import { createGitView } from './git-view'
import { createGitStore } from './git-store'
import { mountSidebar, type SidebarHandle } from '../sidebar'
import type { GitPanelServices } from './git-client'
import type { ActiveOrigin } from '../pane-content'
import { setActiveLocale } from '../i18n/locale-model'
import { ToastHost, clearToasts } from '../ui/toast'

const services: GitPanelServices = {
  open: vi.fn().mockResolvedValue({ state: 'ok', bindingId: 'b1', toplevel: '/r' }),
  status: vi.fn().mockResolvedValue({ status: null, envState: null }),
  diff: vi.fn().mockResolvedValue({ state: 'ok', text: '', truncated: false }),
  log: vi.fn().mockResolvedValue({ log: { entries: [], total: 0, completeness: 'complete' } }),
  stage: vi.fn().mockResolvedValue({ status: null }),
  unstage: vi.fn().mockResolvedValue({ status: null }),
  stageAll: vi.fn().mockResolvedValue({ status: null }),
  unstageAll: vi.fn().mockResolvedValue({ status: null }),
  commit: vi.fn().mockResolvedValue({ state: 'ok', outputTruncated: false }),
  headMessage: vi.fn().mockResolvedValue({ state: 'ok', message: '' }),
  remote: vi.fn().mockResolvedValue({ state: 'none' }),
  openUrl: vi.fn().mockResolvedValue({}),
  close: vi.fn().mockResolvedValue({ closed: true }),
  subscribeGitChanged: vi.fn().mockReturnValue(() => {}),
}

const handles: SidebarHandle[] = []

/** Mount the real view through mountSidebar with no active origin — the
 *  panel's noPane state, so the empty state is on screen immediately. */
function mountGitView() {
  const origin: ActiveOrigin | null = null
  const git = createGitView({
    services,
    store: createGitStore(services),
    opener: { open: () => {} },
    activeOrigin: () => origin,
  })
  const bar = document.createElement('div')
  const panel = document.createElement('div')
  document.body.append(bar, panel)
  handles.push(
    mountSidebar(
      bar,
      panel,
      [git],
      [],
      undefined,
      () => null,
      () => null,
    ),
  )
  render(() => <ToastHost />)
  return panel
}

// The locale signal is module state; every test starts and leaves it English
// so one test's switch cannot decide what the next test reads.
beforeEach(() => {
  setActiveLocale('en')
})

afterEach(() => {
  for (const h of handles) h.destroy()
  handles.length = 0
  setActiveLocale('en')
  vi.clearAllMocks()
  clearToasts()
  document.body.innerHTML = ''
})

describe('git surface renders through the catalogue', () => {
  it('shows the English empty state under en', () => {
    const panel = mountGitView()
    expect(panel.textContent).toContain('No repository to show')
    expect(panel.textContent).toContain(
      'Focus a terminal tab to see the repository your shell is standing in.',
    )
  })

  it('shows Russian under ru', () => {
    setActiveLocale('ru')
    const panel = mountGitView()
    // The empty state, through the catalogue.
    expect(panel.textContent).toContain('Нет репозитория для показа')
    expect(panel.textContent).toContain('Откройте вкладку терминала')
    expect(panel.textContent).not.toContain('No repository to show')
  })

  it('shows the Russian refresh aria-label under ru', () => {
    setActiveLocale('ru')
    const panel = mountGitView()
    // The header action's aria-label is attribute text, not rendered text —
    // a locale switch must reach it too.
    const refresh = panel.querySelector<HTMLButtonElement>('[data-testid="git-refresh"]')
    expect(refresh?.getAttribute('aria-label')).toBe('Обновить git-статус')
  })
})
