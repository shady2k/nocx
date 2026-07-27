// @vitest-environment jsdom
/**
 * Characterization tests for the settings surface.
 *
 * Tests observable behaviour of SettingsContent through its TabContent seam:
 * mount, search, modified-only filter, section navigation, viewport, deep
 * link, and dispose.  These pass against both the old imperative
 * implementation and the new Solid rewrite.
 *
 * Two acceptance-criterion tests are deliberately written to fail against the
 * old code and pass after the rewrite:
 *   - nocx-x6w9: exactly ONE search box and ONE modified filter exist
 *   - nocx-ucxl: clicking a rail section always changes the content pane
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { SettingsContent, SURFACE_SETTINGS, SINGLETON_SETTINGS } from './settings-content'
import { ProfileClient } from './profiles'
import { Dispatcher } from './dispatcher'
import type { Declaration } from './settings'
import type { TabHost } from './tab-content'

// ── Test declarations ────────────────────────────────────────────────
// 5 declarations spanning 3 sections and all control types.

const TEST_DECLARATIONS: Declaration[] = [
  {
    key: 'terminal.fontSize',
    section: 'Terminal',
    label: 'Font Size',
    description: 'Terminal font size in pixels',
    control: 'number',
    dataClass: 'publicConfig',
    default: 14,
    min: 8,
    max: 48,
  },
  {
    key: 'terminal.fontFamily',
    section: 'Terminal',
    label: 'Font Family',
    description: 'CSS font-family value',
    control: 'text',
    dataClass: 'publicConfig',
    default: 'monospace',
  },
  {
    key: 'terminal.cursorStyle',
    section: 'Terminal',
    label: 'Cursor Style',
    description: 'Cursor appearance',
    control: 'select',
    dataClass: 'publicConfig',
    default: 'block',
    options: [
      { value: 'block', label: 'Block' },
      { value: 'bar', label: 'Bar' },
      { value: 'underline', label: 'Underline' },
    ],
  },
  {
    key: 'app.confirmQuit',
    section: 'Application',
    label: 'Confirm Quit',
    description: 'Show confirmation dialog before quitting',
    control: 'toggle',
    dataClass: 'publicConfig',
    default: true,
  },
  {
    key: 'ai.apiKey',
    section: 'AI',
    label: 'API Key',
    description: 'AI provider API key',
    control: 'secret',
    dataClass: 'secretAuthenticator',
  },
]

// ── Helpers ───────────────────────────────────────────────────────────

// jsdom does not implement scrollIntoView. Define it once for all tests.
if (!('scrollIntoView' in HTMLElement.prototype)) {
  Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {
    value: vi.fn(),
    writable: true,
    configurable: true,
  })
}

// PageScroller.scrollToElement calls scrollEl.scrollTo — mock it.
if (!('scrollTo' in HTMLElement.prototype)) {
  Object.defineProperty(HTMLElement.prototype, 'scrollTo', {
    value: vi.fn(),
    writable: true,
    configurable: true,
  })
}

/** Shorthand for mocking client RPCs. */
function mockReady(
  client: ProfileClient,
  overrides: {
    declarations?: Declaration[]
    values?: Record<string, unknown>
    overridden?: string[]
    secrets?: Record<string, boolean>
  } = {},
): void {
  const decls = overrides.declarations ?? TEST_DECLARATIONS
  vi.spyOn(client, 'describeSettings').mockResolvedValue({ declarations: decls })
  vi.spyOn(client, 'getSnapshot').mockResolvedValue({
    values: overrides.values ?? {},
    overridden: overrides.overridden ?? [],
    revision: 0,
  })
  const secretExists = vi.spyOn(client, 'secretExists')
  const secretMap = overrides.secrets ?? {}
  for (const d of decls) {
    if (d.control === 'secret') {
      secretExists.mockResolvedValue({ exists: secretMap[d.key] ?? false })
    }
  }
}
function mockTabHost(): TabHost {
  return {
    setTitle: vi.fn(),
    requestAttention: vi.fn(),
    requestClose: vi.fn(),
  }
}

// ── Tests ─────────────────────────────────────────────────────────────

describe('SettingsContent', () => {
  let target: HTMLDivElement
  let client: ProfileClient
  let content: SettingsContent
  let host: TabHost
  let signal: AbortSignal

  function visibleRows(): HTMLElement[] {
    return Array.from(target.querySelectorAll<HTMLElement>('.ui-settings-row')).filter(
      (r) => r.style.display !== 'none' && !r.classList.contains('st-vis-hidden'),
    )
  }

  beforeEach(() => {
    document.body.replaceChildren()
    target = document.createElement('div')
    document.body.append(target)
    client = new ProfileClient(new Dispatcher())
    content = new SettingsContent(client)
    host = mockTabHost()
    signal = new AbortController().signal
  })

  // ── Surface constants ──────────────────────────────────────────────

  it('exports surface and singleton constants', () => {
    expect(SURFACE_SETTINGS).toBe('nocx.settings')
    expect(SINGLETON_SETTINGS).toBe('nocx.settings')
  })

  // ── Mount ──────────────────────────────────────────────────────────

  it('mounts the Page layout with rail and content', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    // Page renders .ui-page as root; .ui-page__rail and .ui-page__scroll
    // are the rail and scroll containers.
    const page = target.querySelector('.ui-page')
    expect(page).toBeTruthy()
    expect(page!.querySelector('.ui-page__rail')).toBeTruthy()
    expect(page!.querySelector('.ui-page__scroll')).toBeTruthy()
  })

  it('rail has exactly one search input', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    const rail = target.querySelector('.ui-page__rail')!
    const searchInputs = rail.querySelectorAll<HTMLInputElement>('input[type="search"]')
    expect(searchInputs.length).toBe(1)
    expect(searchInputs[0].placeholder).toBe('Search settings…')
  })

  it('rail has one modified-only toggle with count', async () => {
    mockReady(client, {
      values: { 'terminal.fontSize': 18, 'app.confirmQuit': false },
      overridden: ['terminal.fontSize', 'app.confirmQuit'],
    })
    await content.mount(target, host, signal)

    const rail = target.querySelector('.ui-page__rail')!
    const toggles = rail.querySelectorAll<HTMLInputElement>(
      '.ui-settings-filter input[type="checkbox"]',
    )
    expect(toggles.length).toBe(1)
    expect(toggles[0].checked).toBe(false)

    const countSpan = rail.querySelector('.ui-badge-warning')
    expect(countSpan).toBeTruthy()
    expect(countSpan!.textContent).toBe('(2)')
  })

  it('modified-only count excludes secrets', async () => {
    mockReady(client, {
      overridden: ['terminal.fontSize', 'ai.apiKey'],
    })
    await content.mount(target, host, signal)

    const countSpan = target.querySelector('.ui-badge-warning')
    // Only terminal.fontSize — ai.apiKey is a secret and is excluded.
    expect(countSpan!.textContent).toBe('(1)')
  })

  it('section nav lists every generated section in declaration order, then component pages', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    const links = target.querySelectorAll('.ui-settings-section-nav-link')
    const labels = Array.from(links).map((l) => l.textContent.replace(/\s*\d+\s*/, '').trim())

    // Generated sections keep Go's declaration order and stay first — that is
    // the invariant the generated screen depends on. Component pages
    // (nocx-imkb.3 put Connections here) follow them, so asserting the whole
    // list rather than a prefix keeps a stray insertion visible.
    expect(labels).toEqual(['Terminal', 'Application', 'AI', 'Connections'])
  })

  it('section nav shows per-section modified counts', async () => {
    mockReady(client, {
      overridden: ['terminal.fontSize', 'app.confirmQuit'],
    })
    await content.mount(target, host, signal)

    const links = target.querySelectorAll('.ui-settings-section-nav-link')
    const terminalLink = Array.from(links).find((l) => l.textContent.includes('Terminal'))
    const appLink = Array.from(links).find((l) => l.textContent.includes('Application'))
    const aiLink = Array.from(links).find((l) => l.textContent.includes('AI'))

    // Badge is rendered by the Badge kit component as .ui-badge-warning
    expect(terminalLink!.querySelector('.ui-badge-warning')!.textContent).toBe('1')
    expect(appLink!.querySelector('.ui-badge-warning')!.textContent).toBe('1')
    expect(aiLink!.querySelector('.ui-badge-warning')).toBeFalsy()
  })

  // ── Modified-only filter ───────────────────────────────────────────

  it('modified-only toggle filters to overridden non-secret rows', async () => {
    mockReady(client, {
      values: { 'terminal.fontSize': 18 },
      overridden: ['terminal.fontSize'],
    })
    await content.mount(target, host, signal)

    // Before toggle: all 5 rows visible
    expect(visibleRows().length).toBe(5)

    const checkbox = target.querySelector<HTMLInputElement>(
      '.ui-settings-filter input[type="checkbox"]',
    )!
    checkbox.checked = true
    checkbox.dispatchEvent(new Event('change', { bubbles: true }))

    await vi.waitFor(() => {
      // Only terminal.fontSize (overridden, non-secret) visible.
      expect(visibleRows().length).toBe(1)
    })
  })

  // ── Section nav click (nocx-ucxl) ──────────────────────────────────

  it('clicking a section in the rail always changes the content pane', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    // Find the section nav link for Application
    const appLink = Array.from(
      target.querySelectorAll<HTMLButtonElement>('.ui-settings-section-nav-link'),
    ).find((l) => l.textContent.includes('Application'))
    expect(appLink).toBeTruthy()

    // Click Application — content should scroll to that section.
    appLink!.click()

    await vi.waitFor(() => {
      const appItem = appLink!.closest('.ui-settings-section-nav-item')
      expect(appItem!.classList.contains('ui-settings-section-nav-active')).toBe(true)
    })

    // The previously-active nav highlight should have moved.
    const terminalItem = appLink!
      .closest('[aria-label="Settings sections"]')!
      .querySelector('.ui-settings-section-nav-item[data-section="Terminal"]')
    expect(terminalItem!.classList.contains('ui-settings-section-nav-active')).toBe(false)
  })

  // ── Narrow viewport ────────────────────────────────────────────────

  it('Page owns the narrow breakpoint — viewportChanged is a no-op', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    // Page owns the breakpoint via base.css @media (max-width: 640px).
    // There is no Settings-specific narrow state to test.
    // Verify the method exists and does not throw.
    content.viewportChanged({ width: 500, height: 600, devicePixelRatio: 1 })
    content.viewportChanged({ width: 800, height: 600, devicePixelRatio: 1 })
  })

  // ── Search ─────────────────────────────────────────────────────────

  it('search filters rows and sections', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    const searchInput = target.querySelector<HTMLInputElement>('input[type="search"]')!

    searchInput.value = 'font'
    searchInput.dispatchEvent(new Event('input', { bubbles: true }))

    await vi.waitFor(() => {
      expect(visibleRows().length).toBe(2)
    })
  })

  it('scrollToKey clears search and reveals the target row', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    // Set a search filter to hide the target.
    const searchInput = target.querySelector<HTMLInputElement>('input[type="search"]')!
    searchInput.value = 'quitting'
    searchInput.dispatchEvent(new Event('input', { bubbles: true }))

    await vi.waitFor(() => {
      expect(visibleRows().length).toBe(1)
    })

    // Deep-link to a different key.
    content.scrollToKey('terminal.fontFamily')

    expect(searchInput.value).toBe('')

    const row = document.getElementById('st-setting-terminal.fontFamily')
    expect(row).toBeTruthy()

    const control = row!.querySelector<HTMLElement>('input, select, button')
    expect(control).toBeTruthy()
    expect(document.activeElement).toBe(control)
  })

  it('scrollToKey is a no-op for unknown keys', () => {
    expect(() => content.scrollToKey('nonexistent.key')).not.toThrow()
  })

  // ── Dispose ────────────────────────────────────────────────────────

  it('dispose removes content from DOM', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    expect(target.querySelector('.ui-page')).toBeTruthy()
    content.dispose()
    expect(target.querySelector('.ui-page')).toBeFalsy()
  })

  // ── Single instance of search/filter (nocx-x6w9) ────────────────────

  it('has exactly ONE search input in the entire surface (nocx-x6w9)', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    const allSearchInputs = target.querySelectorAll<HTMLInputElement>('input[type="search"]')
    expect(allSearchInputs.length).toBe(1)

    // Also: no search input of type text with search-related placeholder
    const textInputsSearching = target.querySelectorAll<HTMLInputElement>(
      'input[type="text"][placeholder*="Search" i], input[type="text"][placeholder*="search" i]',
    )
    expect(textInputsSearching.length).toBe(0)
  })

  it('has exactly ONE modified-only filter checkbox in the entire surface (nocx-x6w9)', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    const modifiedCheckboxes = target.querySelectorAll<HTMLInputElement>(
      '.ui-settings-filter input[type="checkbox"]',
    )
    expect(modifiedCheckboxes.length).toBe(1)
  })

  // ── Save + filter integration ──────────────────────────────────────

  it('save while filter active: re-activating filter shows saved row', async () => {
    // Set up with fontFamily NOT overridden, fontSize IS overridden.
    mockReady(client, {
      values: { 'terminal.fontSize': 18 },
      overridden: ['terminal.fontSize'],
    })
    vi.spyOn(client, 'setSetting').mockResolvedValue({ ok: true })
    await content.mount(target, host, signal)

    // Activate modified-only filter — only fontSize visible.
    const railCheckbox = target.querySelector<HTMLInputElement>(
      '.ui-settings-filter input[type="checkbox"]',
    )!
    railCheckbox.checked = true
    railCheckbox.dispatchEvent(new Event('change', { bubbles: true }))

    await vi.waitFor(() => {
      expect(visibleRows().length).toBe(1)
    })

    // Deactivate filter, save fontFamily, re-activate.
    railCheckbox.checked = false
    railCheckbox.dispatchEvent(new Event('change', { bubbles: true }))

    await vi.waitFor(() => {
      expect(visibleRows().length).toBe(5)
    })

    // Find and change the fontFamily input.
    const fontFamilyRow = document.getElementById('st-setting-terminal.fontFamily')
    expect(fontFamilyRow).toBeTruthy()
    const fontInput = fontFamilyRow!.querySelector<HTMLInputElement>('input[type="text"]')!
    fontInput.value = 'Fira Code'
    // TextField uses onInput — dispatch input event instead of change.
    fontInput.dispatchEvent(new Event('input', { bubbles: true }))

    await vi.waitFor(() => {
      const rerendered = document.getElementById('st-setting-terminal.fontFamily')
      expect(rerendered).toBeTruthy()
    })

    // Re-activate modified-only — both overridden rows should appear.
    railCheckbox.checked = true
    railCheckbox.dispatchEvent(new Event('change', { bubbles: true }))

    await vi.waitFor(() => {
      // fontSize + fontFamily both overridden → 2 rows
      expect(visibleRows().length).toBe(2)
    })
  })

  // ── Registry: generated-screen invariant (Deliverable 2) ───────────

  it('generated-screen invariant: adding a declaration section auto-creates a nav entry', async () => {
    // A new declaration in a new section appears automatically in the
    // section nav — no frontend code change needed for a Go-declared section.
    const extraDecl: Declaration = {
      key: 'editor.tabSize',
      section: 'Editor',
      label: 'Tab Size',
      description: 'Editor tab width',
      control: 'number',
      dataClass: 'publicConfig',
      default: 4,
      min: 1,
      max: 8,
    }
    mockReady(client, { declarations: [...TEST_DECLARATIONS, extraDecl] })
    await content.mount(target, host, signal)

    const links = target.querySelectorAll('.ui-settings-section-nav-link')
    const labels = Array.from(links).map((l) => l.textContent.replace(/\s*\d+\s*/, '').trim())
    expect(labels).toContain('Editor')
    // Original sections still present.
    expect(labels).toContain('Terminal')
    expect(labels).toContain('Application')
    expect(labels).toContain('AI')
  })
})
