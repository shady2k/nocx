// @vitest-environment jsdom
/**
 * Characterization tests for the settings surface.
 *
 * Tests observable behaviour of SettingsContent through its PaneContent seam:
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
import { createComponent } from 'solid-js'
import { SettingsContent, SURFACE_SETTINGS, SINGLETON_SETTINGS } from './settings-content'
import { ProfileClient } from './profiles'
import { Dispatcher } from './dispatcher'
import type { Declaration, SettingsGroup } from './settings-domain'
import type { PaneHost } from './pane-content'
import { VaultSection } from './vault'
import type { VaultClient } from './vault-client'
import { render, cleanup, fireEvent } from '@solidjs/testing-library'
import { log } from './log'
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

// The rail's group catalogue, mirroring what the Go side declares
// (internal/settings) — the snapshot serves it, the frontend keeps no table
// of its own (nocx-dgsp). The fixture's sections map to groups the way the
// Go catalogue maps Interface/Clipboard/History to application.
const TEST_GROUPS: SettingsGroup[] = [
  { id: 'assistant', title: 'Assistant', order: 0 },
  { id: 'vault', title: 'Vault', order: 1 },
  { id: 'application', title: 'Application', order: 2 },
  { id: 'developer', title: 'Developer', order: 3 },
]
const TEST_SECTION_GROUPS: Record<string, string> = {
  Terminal: 'application',
  Application: 'application',
  AI: 'developer',
  Test: 'developer',
}

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

function mockReady(
  client: ProfileClient,
  overrides: {
    declarations?: Declaration[]
    groups?: SettingsGroup[]
    sectionGroups?: Record<string, string>
    values?: Record<string, unknown>
    overridden?: string[]
    secrets?: Record<string, boolean>
  } = {},
): void {
  const decls = overrides.declarations ?? TEST_DECLARATIONS
  vi.spyOn(client, 'describeSettings').mockResolvedValue({
    declarations: decls,
    groups: overrides.groups ?? TEST_GROUPS,
    sectionGroups: overrides.sectionGroups ?? TEST_SECTION_GROUPS,
  })
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
function mockPaneHost(): PaneHost {
  return {
    setTitle: vi.fn(),
    updateTooltip: vi.fn(),
    requestAttention: vi.fn(),
    requestClose: vi.fn(),
    contentSettled: vi.fn(),
  }
}
/** Click a generated section's rail item. Settings opens on the first rail
 *  page — Connections, a component page — so generated-section assertions
 *  must navigate first, exactly as a user would. */
function openSection(container: HTMLElement, label: string): void {
  const link = Array.from(
    container.querySelectorAll<HTMLButtonElement>('.ui-grouped-nav__item > .ui-button'),
  ).find((l) => l.textContent.includes(label))
  expect(link).toBeTruthy()
  link!.click()
}

// ── Tests ─────────────────────────────────────────────────────────────

describe('SettingsContent', () => {
  let target: HTMLDivElement
  let client: ProfileClient
  let content: SettingsContent
  let host: PaneHost
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
    host = mockPaneHost()
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

    // The screen opens on Connections, a contained-scroll component page
    // with no .ui-page__scroll — open a generated section for the layout
    // containers this test pins.
    openSection(target, 'Terminal')
    await vi.waitFor(() => {
      expect(target.querySelector('.ui-page__scroll')).toBeTruthy()
    })

    // Page renders .ui-page as root; .ui-page__rail and .ui-page__scroll
    // are the rail and scroll containers.
    const page = target.querySelector('.ui-page')
    expect(page).toBeTruthy()
    expect(page!.querySelector('.ui-page__rail')).toBeTruthy()
  })

  it('rail has exactly one search input', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    const rail = target.querySelector('.ui-page__rail')!
    const searchInputs = rail.querySelectorAll<HTMLInputElement>('input[type="search"]')
    expect(searchInputs.length).toBe(1)
    expect(searchInputs[0].placeholder).toBe('Search settings…')
  })

  it('opens on the first rail page — Connections selected, not the first generated section', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    // The rail's first row (Connections, top level) is the current page…
    const nav = target.querySelector('[aria-label="Settings sections"]')!
    const connItem = nav.querySelector<HTMLElement>(
      '.ui-grouped-nav__item[data-item="connections"]',
    )!
    expect(connItem.getAttribute('data-selected')).toBe('true')

    // …and nothing else is: the old default was the first GENERATED section
    // (Terminal in this fixture, History in the shipped catalogue), which
    // sits inside a group while Connections sits at the top of the rail.
    const selected = nav.querySelectorAll<HTMLElement>(
      '.ui-grouped-nav__item[data-selected="true"]',
    )
    expect(selected.length).toBe(1)

    // The body is the Connections page, not the generated sections list.
    expect(target.querySelector('.cm-root')).toBeTruthy()
    expect(target.querySelector('.ui-settings-row')).toBeNull()
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

    const countSpan = rail.querySelector('.ui-badge[data-tone="warning"]')
    expect(countSpan).toBeTruthy()
    // Bare number: the Badge component draws the container now, so the
    // parentheses that used to stand in for one are redundant chrome.
    expect(countSpan!.textContent).toBe('2')
  })

  it('modified-only count excludes secrets', async () => {
    mockReady(client, {
      overridden: ['terminal.fontSize', 'ai.apiKey'],
    })
    await content.mount(target, host, signal)

    const countSpan = target.querySelector('.ui-badge[data-tone="warning"]')
    // Only terminal.fontSize — ai.apiKey is a secret and is excluded.
    expect(countSpan!.textContent).toBe('1')
  })

  it('rail renders group headings, every page under exactly its declared group, ungrouped pages at top level (criterion 1)', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    const nav = target.querySelector('[aria-label="Settings sections"]')!
    const headings = Array.from(nav.querySelectorAll('.ui-grouped-nav__heading')).map(
      (h) => h.textContent,
    )
    expect(headings).toEqual(['Assistant', 'Vault', 'Application', 'Developer'])

    const items = Array.from(nav.querySelectorAll<HTMLElement>('.ui-grouped-nav__item'))
    const labels = items.map((l) => l.textContent.replace(/\s*\d+\s*/, '').trim())
    // Top level first (Connections — a working surface, not a setting), then
    // groups in catalogue order with their members in registry order.
    expect(labels).toEqual([
      'Connections',
      'Endpoints',
      'Protection',
      'Secrets',
      'Terminal',
      'Application',
      'Backup & Restore',
      'Snippets',
      'About',
      'AI',
      'Sandbox access',
    ])

    // Connections is top level: a direct child of the top list, not a group.
    const topList = nav.querySelector('.ui-grouped-nav__list')!
    const first = topList.querySelector(':scope > li') as HTMLElement
    expect(first.getAttribute('data-item')).toBe('connections')
    // And no page appears twice.
    const ids = items.map((l) => l.getAttribute('data-item'))
    expect(new Set(ids).size).toBe(ids.length)
    const sandboxAccess = nav.querySelector('[data-item="sandbox-access"]')!
    expect(sandboxAccess.closest('[data-group="developer"]')).not.toBeNull()
  })

  it('section nav shows per-section modified counts', async () => {
    mockReady(client, {
      overridden: ['terminal.fontSize', 'app.confirmQuit'],
    })
    await content.mount(target, host, signal)

    const links = target.querySelectorAll('.ui-grouped-nav__item > .ui-button')
    const terminalLink = Array.from(links).find((l) => l.textContent.includes('Terminal'))
    const appLink = Array.from(links).find((l) => l.textContent.includes('Application'))
    const aiLink = Array.from(links).find((l) => l.textContent.includes('AI'))

    // Badge is rendered by the Badge kit component with .ui-badge[data-tone="warning"]
    expect(terminalLink!.querySelector('.ui-badge[data-tone="warning"]')!.textContent).toBe('1')
    expect(appLink!.querySelector('.ui-badge[data-tone="warning"]')!.textContent).toBe('1')
    expect(aiLink!.querySelector('.ui-badge[data-tone="warning"]')).toBeFalsy()
  })

  // ── Modified-only filter ───────────────────────────────────────────

  it('modified-only toggle filters to overridden non-secret rows', async () => {
    mockReady(client, {
      values: { 'terminal.fontSize': 18 },
      overridden: ['terminal.fontSize'],
    })
    await content.mount(target, host, signal)

    // The screen opens on the first rail page (Connections, a component
    // page), so open a generated section before counting rows.
    openSection(target, 'Terminal')

    // Before toggle: the three rows of the opened section, not all five
    // settings end to end.
    await vi.waitFor(() => {
      expect(visibleRows().length).toBe(3)
    })

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
      target.querySelectorAll<HTMLButtonElement>('.ui-grouped-nav__item > .ui-button'),
    ).find((l) => l.textContent.includes('Application'))
    expect(appLink).toBeTruthy()

    // Click Application — content should scroll to that section.
    appLink!.click()

    await vi.waitFor(() => {
      const appItem = appLink!.closest('.ui-grouped-nav__item')
      expect(appItem!.getAttribute('data-selected')).toBe('true')
    })

    // The previously-active nav highlight should have moved.
    const terminalItem = appLink!
      .closest('[aria-label="Settings sections"]')!
      .querySelector('.ui-grouped-nav__item[data-item="Terminal"]')
    expect(terminalItem!.hasAttribute('data-selected')).toBe(false)
  })

  // ── Groups (nocx-dgsp) ─────────────────────────────────────────────

  it('Test section renders under Developer (criterion 7)', async () => {
    const testDecl: Declaration = {
      key: 'test.fixture',
      section: 'Test',
      label: 'Fixture',
      description: 'A fixture declaration in the fixture section.',
      control: 'toggle',
      dataClass: 'publicConfig',
      default: true,
    }
    mockReady(client, { declarations: [...TEST_DECLARATIONS, testDecl] })
    await content.mount(target, host, signal)

    const nav = target.querySelector('[aria-label="Settings sections"]')!
    const devGroup = nav.querySelector<HTMLElement>(
      '.ui-grouped-nav__group[data-group="developer"]',
    )!
    expect(devGroup.textContent).toContain('Test')
  })

  it('the vault page is titled Protection and its registry id is still vault (criterion 8)', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    const nav = target.querySelector('[aria-label="Settings sections"]')!
    const vaultItem = nav.querySelector<HTMLElement>('.ui-grouped-nav__item[data-item="vault"]')!
    expect(vaultItem.textContent).toContain('Protection')
    // The id is the address: still 'vault', still under the Vault group.
    const vaultGroup = nav.querySelector<HTMLElement>('.ui-grouped-nav__group[data-group="vault"]')!
    expect(vaultGroup.contains(vaultItem)).toBe(true)
    expect(vaultGroup.textContent).toContain('Secrets')
  })

  it('deep links open their page by id with its group visible, no second address (criterion 5)', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    // newSecret addresses the Secrets page by id — the rail then shows it
    // active under the Vault heading.
    content.startNewSecret('my-secret')
    await vi.waitFor(() => {
      const secretsItem = target.querySelector<HTMLElement>(
        '.ui-grouped-nav__item[data-item="secrets"]',
      )!
      expect(secretsItem.getAttribute('data-selected')).toBe('true')
    })
    const vaultGroup = target.querySelector<HTMLElement>(
      '.ui-grouped-nav__group[data-group="vault"]',
    )!
    expect(vaultGroup.contains(target.querySelector('[data-item="secrets"]'))).toBe(true)

    // newConnection addresses the Connections page by id — top level, no group.
    content.startNewConnection()
    await vi.waitFor(() => {
      const connItem = target.querySelector<HTMLElement>(
        '.ui-grouped-nav__item[data-item="connections"]',
      )!
      expect(connItem.getAttribute('data-selected')).toBe('true')
    })
    const topList = target.querySelector('.ui-grouped-nav__list')!
    expect(topList.querySelector(':scope > [data-item="connections"]')).toBeTruthy()
  })

  it('search results still name their section and search is not scoped to a group (criterion 6)', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    const searchInput = target.querySelector<HTMLInputElement>('input[type="search"]')!
    searchInput.value = 'font'
    searchInput.dispatchEvent(new Event('input', { bubbles: true }))

    await vi.waitFor(() => {
      expect(visibleRows().length).toBe(2)
    })

    // The matching rows sit under their section's heading, still named.
    const visibleSection = Array.from(
      target.querySelectorAll<HTMLElement>('.ui-settings section'),
    ).find((s) => !s.closest('.st-vis-hidden'))
    expect(visibleSection!.querySelector('h2')!.textContent).toBe('Terminal')

    // The rail still shows every group — search narrows rows, never groups.
    const nav = target.querySelector('[aria-label="Settings sections"]')!
    const headings = Array.from(nav.querySelectorAll('.ui-grouped-nav__heading')).map(
      (h) => h.textContent,
    )
    expect(headings).toEqual(['Assistant', 'Vault', 'Application', 'Developer'])
  })

  it('a component page naming a group the catalogue lacks never renders silently at top level (criterion 4)', async () => {
    mockReady(client, { groups: TEST_GROUPS.filter((g) => g.id !== 'vault') })
    await content.mount(target, host, signal)
    // The kit throws on the undeclared id (asserted in grouped-rail.test.tsx);
    // at the surface that means the rail never renders — the malformed
    // catalogue produces no silent top-level 'vault' row and no partial rail.
    expect(target.querySelector('.ui-grouped-nav')).toBeNull()
    expect(target.querySelector('[data-item="vault"]')).toBeNull()
  })

  it('settings.tsx keeps no bespoke rail markup — the kit component owns the nav (criterion 9)', async () => {
    mockReady(client)
    await content.mount(target, host, signal)
    expect(target.querySelector('.ui-settings-section-nav')).toBeNull()
    expect(target.querySelector('nav.ui-grouped-nav[aria-label="Settings sections"]')).toBeTruthy()
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

    // The screen opens on Connections, whose page carries its own search
    // box — open a generated section so the surface holds only the rail's.
    openSection(target, 'Terminal')
    await vi.waitFor(() => {
      expect(target.querySelectorAll<HTMLInputElement>('input[type="search"]').length).toBe(1)
    })

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

    // Open a generated section — the screen opens on Connections, a
    // component page with no settings rows to filter.
    openSection(target, 'Terminal')
    await vi.waitFor(() => {
      expect(visibleRows().length).toBe(3)
    })
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
      // Back to the opened section's three rows, not all five settings.
      expect(visibleRows().length).toBe(3)
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

  it('a section added to a group is a Go-side change with no frontend edit — the snapshot drives the rail (criterion 2)', async () => {
    // Simulate the Go-side change end to end: a new declaration section AND
    // its RegisterSectionGroup call, both served by the describe snapshot.
    // The rail reads the mapping from the snapshot; there is no lookup table
    // in the frontend for a section to fall out of.
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
    mockReady(client, {
      declarations: [...TEST_DECLARATIONS, extraDecl],
      sectionGroups: { ...TEST_SECTION_GROUPS, Editor: 'application' },
    })
    await content.mount(target, host, signal)

    const nav = target.querySelector('[aria-label="Settings sections"]')!
    const labels = Array.from(nav.querySelectorAll<HTMLElement>('.ui-grouped-nav__item')).map((l) =>
      l.textContent.replace(/\s*\d+\s*/, '').trim(),
    )
    expect(labels).toContain('Editor')
    // The new section renders under the Application heading — the mapping
    // arrived from the snapshot, exactly as a Go change would ship it.
    const appGroup = nav.querySelector<HTMLElement>(
      '.ui-grouped-nav__group[data-group="application"]',
    )!
    expect(appGroup.textContent).toContain('Editor')
    // Original sections still present.
    expect(labels).toContain('Terminal')
    expect(labels).toContain('Application')
    expect(labels).toContain('AI')
  })

  // ── Number rows: unit suffix, range caption, out-of-range error (nocx-w7h.7) ──

  it('number rows render the unit beside the value, the range caption beneath, and the error in the same slot', async () => {
    const historyDecls: Declaration[] = [
      {
        key: 'history.retentionDays',
        section: 'History',
        label: 'Keep history for',
        description: 'How long a completed command is kept.',
        control: 'number',
        dataClass: 'publicConfig',
        default: 0,
        min: 0,
        max: 3650,
        unit: 'days',
      },
      {
        key: 'history.retentionMiB',
        section: 'History',
        label: 'Command history size',
        description: 'How much command text to keep.',
        control: 'number',
        dataClass: 'publicConfig',
        default: 4096,
        min: 64,
        max: 1048576,
        unit: 'MiB',
      },
      {
        key: 'history.diskCeilingMiB',
        section: 'History',
        label: 'Disk space limit',
        description: 'Physical ceiling for the history database plus its write-ahead log.',
        control: 'number',
        dataClass: 'publicConfig',
        default: 8192,
        min: 128,
        max: 2097152,
        unit: 'MiB',
      },
    ]
    mockReady(client, {
      declarations: historyDecls,
      values: {
        'history.retentionDays': 30,
        'history.retentionMiB': 4096,
        'history.diskCeilingMiB': 8192,
      },
    })
    await content.mount(target, host, signal)
    // The screen opens on Connections — open the History section, the only
    // one this fixture declares, before reading its rows.
    openSection(target, 'History')
    await vi.waitFor(() => {
      expect(target.querySelectorAll('.ui-text-field__unit').length).toBe(3)
    })

    // The units the owner will read: days, MiB, MiB — beside each value.
    const units = Array.from(target.querySelectorAll('.ui-text-field__unit')).map(
      (e) => e.textContent,
    )
    expect(units).toEqual(['days', 'MiB', 'MiB'])

    // The range caption beneath each field, read from the declaration's
    // Min/Max — and the old floating bounds span is gone.
    const captions = Array.from(target.querySelectorAll('.ui-text-field__caption')).map(
      (e) => e.textContent,
    )
    expect(captions).toEqual(['0 – 3650 days', '64 – 1048576 MiB', '128 – 2097152 MiB'])
    // (retentionDays is 30 here — an ordinary number, so the range shows.)
    expect(target.querySelector('.ui-settings-bounds')).toBeNull()

    // A value outside the range replaces the caption with the error in the
    // same slot — still exactly one slot per field.
    const daysInput = Array.from(
      target.querySelectorAll<HTMLInputElement>('input[type="number"]'),
    )[0]
    fireEvent.input(daysInput, { target: { value: '5000' } })
    await vi.waitFor(() => {
      const slot = target.querySelector('.ui-text-field__caption[data-tone="error"]')
      expect(slot?.textContent).toBe('Must be at most 3650 days')
    })
    expect(target.querySelectorAll('.ui-text-field__caption').length).toBe(3)
  })

  // displayValue exists to turn a value into the TEXT of a control, and it
  // warns when it can find neither a usable value nor a usable default —
  // which is precisely what a boolean looks like to it. Routing the
  // range/error checks through it for EVERY row therefore printed
  // "unusable value and default for setting history.enabled, got boolean,
  // defaultType boolean" on every mount. Reported from the console, where
  // it is the only place it shows.
  it('mounting a page of toggles logs nothing', async () => {
    const warn = vi.spyOn(log, 'warn')
    mockReady(client, {
      declarations: [
        {
          key: 'history.enabled',
          section: 'History',
          label: 'Keep command history',
          description: 'Record commands for recall after a restart.',
          control: 'toggle',
          dataClass: 'publicConfig',
          default: true,
        },
        {
          key: 'history.outputEnabled',
          section: 'History',
          label: 'Keep command output',
          description: 'Whether the text commands printed is kept.',
          control: 'toggle',
          dataClass: 'publicConfig',
          default: true,
        },
      ],
      values: { 'history.enabled': true, 'history.outputEnabled': false },
    })
    await content.mount(target, host, signal)
    openSection(target, 'History')
    await vi.waitFor(() => {
      expect(target.querySelectorAll('.ui-settings-row .ui-checkbox').length).toBe(2)
    })

    // Two rows, each with its switch (the rail's own modified filter is a
    // checkbox too, so scope the count to the rows).
    expect(target.querySelectorAll('.ui-settings-row .ui-checkbox').length).toBe(2)
    expect(warn).not.toHaveBeenCalled()
  })

  // The value the owner actually sees on a fresh install is the sentinel,
  // and "0" above "0 – 3650 days" says nothing about what zero does. The
  // caption slot explains it until the number becomes an ordinary one.
  it('a number sitting at its declared sentinel reads what the sentinel means', async () => {
    const decl: Declaration = {
      key: 'history.retentionDays',
      section: 'History',
      label: 'Keep history for',
      description: 'How long a completed command is kept.',
      control: 'number',
      dataClass: 'publicConfig',
      default: 0,
      min: 0,
      max: 3650,
      unit: 'days',
      zeroLabel: 'Kept until the size limit is reached',
    }
    mockReady(client, { declarations: [decl], values: { 'history.retentionDays': 0 } })
    await content.mount(target, host, signal)
    openSection(target, 'History')
    await vi.waitFor(() => {
      expect(target.querySelector('.ui-text-field__caption')).toBeTruthy()
    })

    const slot = () => target.querySelector('.ui-text-field__caption')
    expect(slot()?.textContent).toBe('Kept until the size limit is reached')
    // Still the quiet caption tone, not an error — zero is a valid value.
    expect(slot()?.getAttribute('data-tone')).toBe('caption')

    // Type a real number and the range comes back in the same slot.
    const input = target.querySelector<HTMLInputElement>('input[type="number"]')!
    fireEvent.input(input, { target: { value: '30' } })
    await vi.waitFor(() => {
      expect(slot()?.textContent).toBe('0 – 3650 days')
    })
    expect(target.querySelectorAll('.ui-text-field__caption').length).toBe(1)
  })

  // Both layers reject an out-of-range number — the screen from the
  // declaration's Min/Max, the backend because it is the authority. Only one
  // of them talks to the user. Found in a real browser, not here: the
  // backend's `settings: "history.diskCeilingMiB" validation failed: value 1
  // below minimum 128` rendered directly under the caption that already said
  // "Must be at least 128 MiB", in the backend's language and wider than the
  // field's column.
  it('the backend rejection is not repeated under a field whose caption already says it', async () => {
    const decl: Declaration = {
      key: 'history.diskCeilingMiB',
      section: 'History',
      label: 'Disk space limit',
      description: 'Physical ceiling for the history database.',
      control: 'number',
      dataClass: 'publicConfig',
      default: 8192,
      min: 128,
      max: 2097152,
      unit: 'MiB',
    }
    mockReady(client, { declarations: [decl], values: { 'history.diskCeilingMiB': 8192 } })
    const backendMessage =
      'settings: "history.diskCeilingMiB" validation failed: value 1 below minimum 128'
    vi.spyOn(client, 'setSetting').mockRejectedValue(new Error(backendMessage))
    await content.mount(target, host, signal)
    openSection(target, 'History')
    await vi.waitFor(() => {
      expect(target.querySelector('input[type="number"]')).toBeTruthy()
    })

    const input = target.querySelector<HTMLInputElement>('input[type="number"]')!
    fireEvent.input(input, { target: { value: '1' } })

    await vi.waitFor(() => {
      const slot = target.querySelector('.ui-text-field__caption[data-tone="error"]')
      expect(slot?.textContent).toBe('Must be at least 128 MiB')
    })
    expect(target.querySelector('.ui-settings-error')).toBeNull()
    expect(target.textContent).not.toContain('validation failed')
  })

  // The narrowness of that suppression is the point: a rejection the screen
  // could NOT predict still reaches the user verbatim, because a save that
  // fails silently is the worse defect.
  it('a rejection the declaration does not predict still reaches the user verbatim', async () => {
    const decl: Declaration = {
      key: 'history.diskCeilingMiB',
      section: 'History',
      label: 'Disk space limit',
      description: 'Physical ceiling for the history database.',
      control: 'number',
      dataClass: 'publicConfig',
      default: 8192,
      min: 128,
      max: 2097152,
      unit: 'MiB',
    }
    mockReady(client, { declarations: [decl], values: { 'history.diskCeilingMiB': 8192 } })
    vi.spyOn(client, 'setSetting').mockRejectedValue(new Error('settings: store is read-only'))
    await content.mount(target, host, signal)
    openSection(target, 'History')
    await vi.waitFor(() => {
      expect(target.querySelector('input[type="number"]')).toBeTruthy()
    })

    // In range — the caption slot has nothing to say, so the surface must.
    const input = target.querySelector<HTMLInputElement>('input[type="number"]')!
    fireEvent.input(input, { target: { value: '4096' } })

    await vi.waitFor(() => {
      expect(target.querySelector('.ui-settings-error')?.textContent).toBe(
        'settings: store is read-only',
      )
    })
    expect(target.querySelector('.ui-text-field__caption[data-tone="error"]')).toBeNull()
  })
})

describe('horizontal Field gate — every settings row must use primary label', () => {
  let target: HTMLDivElement
  let client: ProfileClient
  let content: SettingsContent
  let host: PaneHost
  let signal: AbortSignal

  beforeEach(() => {
    document.body.replaceChildren()
    target = document.createElement('div')
    document.body.append(target)
    client = new ProfileClient(new Dispatcher())
    content = new SettingsContent(client)
    host = mockPaneHost()
    signal = new AbortController().signal
  })

  it('mounts generated settings pages with every horizontal Field defaulting to data-label=primary', async () => {
    mockReady(client)
    await content.mount(target, host, signal)
    openSection(target, 'Terminal')
    await vi.waitFor(() => {
      expect(target.querySelectorAll<HTMLElement>('.ui-field-horizontal').length).toBeGreaterThan(0)
    })

    const horizontals = target.querySelectorAll<HTMLElement>('.ui-field-horizontal')
    expect(horizontals.length).toBeGreaterThan(0)
    for (const el of horizontals) {
      expect(el.getAttribute('data-label')).toBe('primary')
    }
  })

  it('every vault-section horizontal Field defaults to data-label=primary', () => {
    const vaultStatus = {
      state: 'unsealed' as const,
      osKeyAvailable: true,
      osKeyCapable: true,
      hasPassphrase: true,
      autoSealMinutes: 15,
      providers: [{ id: 'test-provider', writable: true, ready: true, reason: undefined }],
      defaultProvider: 'test-provider',
    }
    const vaultController = {
      status: () => vaultStatus,
      refresh: vi.fn().mockResolvedValue(true),
      seal: vi.fn().mockResolvedValue(undefined),
      setDefaultProvider: vi.fn().mockResolvedValue(undefined),
      showSetup: vi.fn().mockReturnValue(false),
      showUnlock: vi.fn().mockReturnValue(false),
      unlockReason: vi.fn().mockReturnValue(null),
      ensureBeforeSave: vi.fn(),
      onSetupDone: vi.fn(),
      onUnsealDone: vi.fn(),
      openUnlock: vi.fn(),
      openSetup: vi.fn(),
      closeSetup: vi.fn(),
      closeUnlock: vi.fn(),
      saveSecretWithVault: vi.fn(),
      changePassphrase: vi.fn(),
      regenerateRecovery: vi.fn(),
    }
    const vaultClient = {
      status: vi.fn(),
      setup: vi.fn(),
      unseal: vi.fn(),
      seal: vi.fn(),
      changePassphrase: vi.fn(),
      regenerateRecovery: vi.fn(),
      setDefaultProvider: vi.fn(),
      setAutoSeal: vi.fn().mockResolvedValue(undefined),
      activity: vi.fn(),
    } as unknown as VaultClient

    const { container } = render(() =>
      createComponent(VaultSection, { vaultClient, vaultController }),
    )

    try {
      const horizontals = container.querySelectorAll<HTMLElement>('.ui-field-horizontal')
      expect(horizontals.length).toBeGreaterThan(0)
      for (const el of horizontals) {
        expect(el.getAttribute('data-label')).toBe('primary')
      }
    } finally {
      cleanup()
    }
  })
})
