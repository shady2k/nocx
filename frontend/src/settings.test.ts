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
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createComponent } from 'solid-js'
import { SettingsContent, SURFACE_SETTINGS, SINGLETON_SETTINGS } from './settings-content'
import { systemPromptText } from './systemprompt'
import { SettingsObserver } from './settings-observer'
import { ProfileClient, type RestorePreview } from './profiles'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import type { Declaration, SettingsGroup } from './settings-domain'
import type { PaneHost } from './pane-content'
import { VaultSection, createVaultSecretSource, type VaultController } from './vault'
import type { VaultClient } from './vault-client'
import { render, cleanup, fireEvent } from '@solidjs/testing-library'
import { log } from './log'
import { SkillsStore, type SkillsClientLike } from './skills-store'
// ── Test declarations ────────────────────────────────────────────────
// 5 declarations spanning 3 sections and all control types.

const toasts: { message: string; level?: string }[] = []
vi.mock('./ui/toast', () => ({
  showToast: (t: { message: string; level?: string }) => {
    toasts.push(t)
  },
}))

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
    toasts.length = 0
    target = document.createElement('div')
    document.body.append(target)
    client = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))
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

  /**
   * A SETTINGS CHANGE MUST NOT TAKE BACK WHAT THE PERSON JUST DID (nocx-hphhh).
   *
   * Every settings write broadcasts settings.changed and the observer answers
   * it by refetching the whole snapshot, while the person goes on using the
   * page. What a refresh must not do is rebuild that page: a chosen file lives
   * on the input element and nowhere else, so a re-created input is a
   * selection silently taken back, with the surface then saying "No file
   * selected" and nothing to explain it.
   *
   * The property is one memo identity away from being false, which is why it
   * is pinned here rather than trusted. `settingsPages()` builds its page
   * objects fresh on every run, `activePage()` finds one in that array, and
   * the body renders it through a `keyed` Show — and keying re-creates the
   * subtree whenever the identity changes, deliberately, so that switching
   * pages replaces the body. A refresh replaces the declarations array, which
   * recomputes every one of those objects, so the page's identity has to be
   * its ID rather than the object that describes it.
   *
   * THE MOCKS HAND BACK FRESH OBJECTS ON PURPOSE. Every answer off the wire is
   * parsed from its own JSON, so nothing the renderer stores is ever
   * reference-equal to what it stored before. A mockResolvedValue returns one
   * object to every call, which stops the second snapshot from propagating at
   * all — the test then passes without ever exercising a refresh, which is
   * what the first draft of it did.
   *
   * AND IT WAITS FOR THE REFRESH TO LAND, not for it to start. `getSnapshot`
   * is called at the top of `refresh`, before a single store write; a test
   * that waits on the call count asserts in the gap between the request and
   * the rebuild it triggers, and passes whatever the rebuild does. The second
   * answer therefore carries a section the first did not, and the assertion
   * comes after that section is on the rail — the one moment where the whole
   * of the refresh is known to have been applied.
   */
  it('a settings refresh does not take back the file you chose', async () => {
    let invalidate: ((params: unknown) => void) | null = null
    const dispatcher = {
      subscribe: (method: string, cb: (params: unknown) => void) => {
        if (method === 'settings.changed') invalidate = cb
        return () => {}
      },
      onConnect: () => () => {},
    }
    const observer = new SettingsObserver(dispatcher as unknown as Dispatcher)
    content = new SettingsContent(client, observer)
    mockReady(client)
    // Fresh objects per call, because that is what the wire gives: every
    // answer is parsed from its own JSON, so no array or record the renderer
    // stores is ever reference-equal to the one before it. A mock that hands
    // back one object hides exactly the propagation this test is about.
    // The second answer carries one declaration the first did not, in a
    // section of its own. That is what the assertion below waits for: a rail
    // row that can only exist once `setDeclarations` has propagated all the
    // way through the page registry — the same propagation that would rebuild
    // the body if the body were keyed on the page object.
    const LATE_SECTION = 'Latecomer'
    let describeCalls = 0
    vi.spyOn(client, 'describeSettings').mockImplementation(() => {
      describeCalls++
      const declarations = TEST_DECLARATIONS.map((d) => ({ ...d }))
      if (describeCalls > 1) {
        declarations.push({
          key: 'late.arrival',
          section: LATE_SECTION,
          label: 'Late Arrival',
          description: 'A setting the first describe did not carry',
          control: 'toggle',
          dataClass: 'publicConfig',
          default: false,
        })
      }
      return Promise.resolve({
        declarations,
        groups: TEST_GROUPS.map((g) => ({ ...g })),
        sectionGroups: { ...TEST_SECTION_GROUPS, [LATE_SECTION]: 'application' },
      })
    })
    vi.spyOn(client, 'getSnapshot').mockImplementation(() =>
      Promise.resolve({
        values: {},
        overridden: [],
        revision: 0,
      }),
    )
    vi.spyOn(client, 'previewBackupRestore').mockResolvedValue({
      strategy: 'merge',
      settings: { changed: 1, unchanged: 0 },
      connections: {
        profiles: { added: 0, updated: 0, unchanged: 0 },
        groups: { added: 0, updated: 0, unchanged: 0 },
      },
    } as unknown as RestorePreview)
    await content.mount(target, host, signal)

    openSection(target, 'Backup & Restore')
    const input = await vi.waitFor(() => {
      const el = target.querySelector<HTMLInputElement>('.ui-file-input__native')
      expect(el).toBeTruthy()
      return el!
    })

    const file = new File(['{}'], 'backup.json', { type: 'application/json' })
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    fireEvent.change(input)
    await vi.waitFor(() => {
      expect(target.querySelector('.ui-file-input__name')!.textContent).toBe('backup.json')
    })

    // Somebody's settings write comes back — their own, a moment ago, or
    // another window's. Either way it is news about values, not a reason to
    // rebuild the page they are working in.
    expect(invalidate).toBeTruthy()
    invalidate!({ revision: 1, keys: ['tab.placement'] })
    await vi.waitFor(() => {
      const rows = Array.from(
        target.querySelectorAll<HTMLButtonElement>('.ui-grouped-nav__item > .ui-button'),
      )
      expect(rows.some((r) => r.textContent.includes(LATE_SECTION))).toBe(true)
    })

    expect(target.querySelector('.ui-file-input__name')!.textContent).toBe('backup.json')
  })

  /**
   * AND THE PREVIEW IT PRODUCED SURVIVES THE SAME REFRESH (nocx-3icu1).
   *
   * The test above stops at the file input, which is the first thing a refresh
   * could take back. It is not the last, and it is not what CI actually caught.
   * The webkit trace for run 33277630612 failed on the Merge button: the
   * preview heading was on screen at one frame snapshot and the whole section
   * said "No file selected" fourteen milliseconds later. File input and preview
   * go together because they are one component, but only this one watches the
   * half a person acts on.
   *
   * THE PREVIEW MOCK IS COMPLETE, AND THE ONE ABOVE IS NOT. A partial payload
   * throws inside the preview block on the first field it does not carry —
   * `connectionsRequiringCredential.length` reaches it before anything is
   * drawn — so a test with one never renders the preview at all and cannot
   * report that it vanished.
   *
   * It waits for the refresh the same way its neighbour does, on the late
   * section reaching the rail. One way of knowing a refresh has landed, not
   * two.
   */
  it('a settings refresh does not take back the preview you are looking at', async () => {
    let invalidate: ((params: unknown) => void) | null = null
    const dispatcher = {
      subscribe: (method: string, cb: (params: unknown) => void) => {
        if (method === 'settings.changed') invalidate = cb
        return () => {}
      },
      onConnect: () => () => {},
    }
    const observer = new SettingsObserver(dispatcher as unknown as Dispatcher)
    content = new SettingsContent(client, observer)
    mockReady(client)
    const LATE_SECTION = 'Latecomer'
    let describeCalls = 0
    vi.spyOn(client, 'describeSettings').mockImplementation(() => {
      describeCalls++
      const declarations = TEST_DECLARATIONS.map((d) => ({ ...d }))
      if (describeCalls > 1) {
        declarations.push({
          key: 'late.arrival',
          section: LATE_SECTION,
          label: 'Late Arrival',
          description: 'A setting the first describe did not carry',
          control: 'toggle',
          dataClass: 'publicConfig',
          default: false,
        })
      }
      return Promise.resolve({
        declarations,
        groups: TEST_GROUPS.map((g) => ({ ...g })),
        sectionGroups: { ...TEST_SECTION_GROUPS, [LATE_SECTION]: 'application' },
      })
    })
    vi.spyOn(client, 'getSnapshot').mockImplementation(() =>
      Promise.resolve({ values: {}, overridden: [], revision: 0 }),
    )
    vi.spyOn(client, 'previewBackupRestore').mockImplementation(() =>
      Promise.resolve({
        previewToken: 'tok-1',
        createdAt: '2026-08-30T00:00:00Z',
        strategy: 'merge',
        settings: { included: 1, changed: 1, reset: 0 },
        connections: { included: 0, added: 0, updated: 0, removed: 0 },
        groups: { included: 0, added: 0, updated: 0, removed: 0 },
        snippets: { included: 0 },
        notes: { included: 0 },
        skills: { included: 0 },
        connectionsRequiringCredential: [],
        omissions: {
          credentialBindingsRemoved: 0,
          groupCredentialBindingsRemoved: 0,
          groupDefaultKeysOmitted: 0,
        },
      }),
    )
    await content.mount(target, host, signal)

    openSection(target, 'Backup & Restore')
    const input = await vi.waitFor(() => {
      const el = target.querySelector<HTMLInputElement>('.ui-file-input__native')
      expect(el).toBeTruthy()
      return el!
    })

    const file = new File(['{}'], 'backup.json', { type: 'application/json' })
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    fireEvent.change(input)

    const mergeButton = (): HTMLElement | undefined =>
      Array.from(target.querySelectorAll<HTMLElement>('button')).find(
        (b) => b.textContent.trim() === 'Merge backup',
      )
    await vi.waitFor(() => {
      expect(mergeButton()).toBeTruthy()
    })

    expect(invalidate).toBeTruthy()
    invalidate!({ revision: 1, keys: ['tab.placement'] })
    await vi.waitFor(() => {
      const rows = Array.from(
        target.querySelectorAll<HTMLButtonElement>('.ui-grouped-nav__item > .ui-button'),
      )
      expect(rows.some((r) => r.textContent.includes(LATE_SECTION))).toBe(true)
    })

    expect(mergeButton()).toBeTruthy()
    expect(target.querySelector('.ui-file-input__name')!.textContent).toBe('backup.json')
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
      'Roles',
      'Agent policy',
      'Protection',
      'Secrets',
      'Terminal',
      'Application',
      'Backup & Restore',
      'Snippets',
      'Skills',
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

  it('Skills rail navigation mounts the Skills page', async () => {
    const skillsClient: SkillsClientLike = {
      list: vi.fn().mockResolvedValue({
        documentPath: '/tmp/nocx/skills.json',
        skills: [
          {
            name: 'deploy',
            description: 'Deploy the service',
            provenance: 'authored',
            path: '/tmp/nocx/skills/deploy/SKILL.md',
            enabled: true,
            status: 'approved',
          },
        ],
      }),
      setEnabled: vi.fn().mockResolvedValue({ name: 'deploy', enabled: false }),
      remove: vi.fn().mockResolvedValue({ name: 'deploy' }),
      approve: vi.fn().mockResolvedValue({ name: 'deploy', status: 'approved' }),
    }
    const skillsStore = new SkillsStore(skillsClient)
    content = new SettingsContent(
      client,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      undefined,
      skillsStore,
    )
    mockReady(client)
    await content.mount(target, host, signal)

    openSection(target, 'Skills')
    await vi.waitFor(() => {
      expect(target.querySelector('[data-skill-name="deploy"]')).toBeTruthy()
    })
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

  // ── What is in the page, not merely what is on it ──────────────────

  it('a section the person is not looking at is not in the page at all', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    openSection(target, 'Terminal')
    await vi.waitFor(() => {
      expect(visibleRows().length).toBe(3)
    })

    // Every row the document holds belongs to the open section. The surface
    // already answers "is this page showing?" by unmounting for a component
    // page; a generated section is the same question and gets the same
    // answer, so nothing off-page is left behind the open one.
    const rows = Array.from(target.querySelectorAll<HTMLElement>('.ui-settings-row'))
    expect(rows.map((r) => r.dataset.key)).toEqual([
      'terminal.fontSize',
      'terminal.fontFamily',
      'terminal.cursorStyle',
    ])

    // Stated as the thing the browser proof measures: the LAST row in the
    // document is a row the person can see. It was true by accident until a
    // section registered after Interface put a display:none row behind the
    // open page and the e2e scroll proofs measured that one instead
    // (nocx-avogl.4).
    const seen = visibleRows()
    expect(rows[rows.length - 1]).toBe(seen[seen.length - 1])

    // …and the same for the section headings.
    const headings = Array.from(target.querySelectorAll<HTMLElement>('.ui-page-section h2')).map(
      (h) => h.textContent,
    )
    expect(headings).toEqual(['Terminal'])
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
    content.viewportChanged({ width: 500, height: 600 })
    content.viewportChanged({ width: 800, height: 600 })
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
  // The caption slot is the field's own home for the range fact, and it is
  // the only one: a refusal the surface already predicts must not ALSO be
  // raised as the outcome of the save — the same fact twice, once under the
  // field and once in a toast, is the double-fact defect fieldSaveError's
  // comment records.
  it('a refusal the caption already predicts stays on the field and is not toasted', async () => {
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
    expect(target.textContent).not.toContain('validation failed')
    // The Connections page that mounts first may raise its own toast; none
    // of them may be the settings-save outcome, which the caption already
    // owns on the field.
    expect(toasts.some((t) => t.message.includes('Could not save'))).toBe(false)
  })

  // The narrowness of that suppression is the point: a rejection the screen
  // could NOT predict still reaches the user — now as the outcome of the
  // save they triggered, in the toast home, mapped out of the backend's
  // `settings:` prefix into a sentence (ui/README.md "Toast"; a backend
  // string passed through untouched is the defect malformed-reason.ts exists
  // to prevent).
  it('a rejection the declaration does not predict reaches the user as a toast', async () => {
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

    // In range — the caption slot has nothing to say, so the outcome of the
    // save must.
    const input = target.querySelector<HTMLInputElement>('input[type="number"]')!
    fireEvent.input(input, { target: { value: '4096' } })

    await vi.waitFor(() => {
      expect(toasts.some((t) => t.level === 'danger' && t.message.includes('Could not save'))).toBe(
        true,
      )
    })
    const toast = toasts.find((t) => t.message.includes('Could not save'))
    expect(toast?.message).toBe(
      'Could not save "Disk space limit": This setting could not be saved — the store is read-only',
    )
  })
})

// ═══════════════════════════════════════════════════════════════════════════
//  The routing matrix (nocx-3mniv task 4)
//
//  The criterion that is easy to fake: NOTHING in the renderer enumerates a
//  kind or a channel. The proof is not a reading of the code — it is the
//  invented-declaration test below, which adds a kind and a channel this
//  repository has never heard of and watches them appear.
// ═══════════════════════════════════════════════════════════════════════════

/** One cell of the routing matrix, in the shape internal/notify declares it:
 *  key `notifications.route.<kind>.<channel>`, label `<kind> → <channel>`. */
function routeCell(
  kind: string,
  channel: string,
  kindLabel: string,
  channelLabel: string,
  defaultOn = false,
): Declaration {
  return {
    key: `notifications.route.${kind}.${channel}`,
    section: 'Notifications',
    label: `${kindLabel} → ${channelLabel}`,
    description: `${kindLabel}. When on, it reaches ${channelLabel}.`,
    control: 'toggle',
    dataClass: 'publicConfig',
    default: defaultOn,
  }
}

const NOTIFY_DECLARATIONS: Declaration[] = [
  routeCell('bell', 'banner', 'Terminal bell', 'OS banner'),
  routeCell('bell', 'toast', 'Terminal bell', 'In-app toast'),
  routeCell('programNotify', 'banner', 'Program notification request', 'OS banner', true),
  routeCell('programNotify', 'toast', 'Program notification request', 'In-app toast', true),
  {
    key: 'notifications.debounceMs',
    section: 'Notifications',
    label: 'Quiet window',
    description: 'How long one notification silences the next.',
    control: 'number',
    dataClass: 'publicConfig',
    default: 500,
    min: 0,
    max: 60000,
    unit: 'ms',
  },
]

const NOTIFY_SECTION_GROUPS: Record<string, string> = {
  ...TEST_SECTION_GROUPS,
  Notifications: 'application',
}

describe('notification routing matrix (nocx-3mniv)', () => {
  let target: HTMLDivElement
  let client: ProfileClient
  let content: SettingsContent
  let host: PaneHost
  let signal: AbortSignal

  beforeEach(() => {
    document.body.replaceChildren()
    target = document.createElement('div')
    document.body.append(target)
    client = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))
    content = new SettingsContent(client)
    host = mockPaneHost()
    signal = new AbortController().signal
  })

  async function openNotifications(declarations: Declaration[] = NOTIFY_DECLARATIONS) {
    // The snapshot carries EFFECTIVE values, defaults included — that is what
    // Registry.GetSnapshot sends, and a fixture that omitted them would show
    // every shipped-on cell as off and prove nothing.
    const values: Record<string, unknown> = {}
    for (const d of declarations) values[d.key] = d.default
    mockReady(client, {
      declarations: [...TEST_DECLARATIONS, ...declarations],
      sectionGroups: NOTIFY_SECTION_GROUPS,
      values,
    })
    await content.mount(target, host, signal)
    openSection(target, 'Notifications')
    await vi.waitFor(() => {
      expect(target.querySelector('.ui-toggle-matrix')).toBeTruthy()
    })
    return target.querySelector<HTMLTableElement>('.ui-toggle-matrix')!
  }

  /** The row labels currently on screen — a hidden row is marked on its
   *  `<tr>`, which is what `display: none` needs to hide the whole row. */
  function visibleRowLabels(matrix: HTMLElement): string[] {
    return Array.from(
      matrix.querySelectorAll(
        '.ui-toggle-matrix__row:not([data-hidden="true"]) .ui-toggle-matrix__row-header',
      ),
    ).map((e) => e.textContent ?? '')
  }

  /** The column labels currently on screen. */
  function visibleColumnLabels(matrix: HTMLElement): string[] {
    return Array.from(
      matrix.querySelectorAll('.ui-toggle-matrix__column:not([data-hidden="true"])'),
    ).map((e) => e.textContent ?? '')
  }

  it('reads as kind × channel, not as one sentence per pair', async () => {
    const matrix = await openNotifications()

    expect(visibleRowLabels(matrix)).toEqual(['Terminal bell', 'Program notification request'])
    expect(visibleColumnLabels(matrix)).toEqual(['OS banner', 'In-app toast'])
    // Four pairs, four switches — and none of them is a settings row of its
    // own any more.
    expect(matrix.querySelectorAll('.ui-toggle-matrix__cell input[type="checkbox"]').length).toBe(4)
    expect(document.getElementById('st-setting-notifications.route.bell.banner')).toBeTruthy()
    expect(
      target.querySelector('.ui-settings-row[data-key="notifications.route.bell.banner"]'),
    ).toBeNull()
  })

  // THE criterion. A kind and a channel that exist nowhere in this repository
  // — not in the catalogue, not in the renderer, not in this fixture's other
  // tests — appear because the backend declared them.
  it('a kind and a channel this code has never heard of appear because the backend declared them', async () => {
    const matrix = await openNotifications([
      ...NOTIFY_DECLARATIONS,
      routeCell('quotaExceeded', 'banner', 'A quota was exceeded', 'OS banner'),
      routeCell('quotaExceeded', 'carrierPigeon', 'A quota was exceeded', 'Carrier pigeon'),
    ])

    expect(visibleRowLabels(matrix)).toEqual([
      'Terminal bell',
      'Program notification request',
      'A quota was exceeded',
    ])
    expect(visibleColumnLabels(matrix)).toEqual(['OS banner', 'In-app toast', 'Carrier pigeon'])
    const invented = matrix.querySelector<HTMLElement>(
      '.ui-toggle-matrix__cell[data-row="quotaExceeded"][data-column="carrierPigeon"]',
    )!
    expect(invented.querySelector('input[type="checkbox"]')).toBeTruthy()

    // And a pair the backend does NOT declare is an empty cell — absent
    // rather than offered and declined (ADR-0047 §3).
    const notOffered = matrix.querySelector<HTMLElement>(
      '.ui-toggle-matrix__cell[data-row="bell"][data-column="carrierPigeon"]',
    )!
    expect(notOffered.querySelector('input')).toBeNull()
  })

  it('every switch has an accessible name that stands on its own, under both headers', async () => {
    const matrix = await openNotifications()

    const names = Array.from(
      matrix.querySelectorAll<HTMLInputElement>('.ui-toggle-matrix__cell input[type="checkbox"]'),
    ).map((i) => i.getAttribute('aria-label'))
    expect(names).toEqual([
      'Terminal bell → OS banner',
      'Terminal bell → In-app toast',
      'Program notification request → OS banner',
      'Program notification request → In-app toast',
    ])
    // The headers are what locate the switch; without scope a screen reader
    // gets a grid of switches and no idea where any of them sits.
    expect(
      Array.from(matrix.querySelectorAll('.ui-toggle-matrix__row-header')).every(
        (h) => h.getAttribute('scope') === 'row',
      ),
    ).toBe(true)
    expect(
      Array.from(matrix.querySelectorAll('.ui-toggle-matrix__column')).every(
        (h) => h.getAttribute('scope') === 'col',
      ),
    ).toBe(true)
  })

  it('the shipped defaults show as the state of the cells, and flipping one saves its own key', async () => {
    const setSetting = vi.spyOn(client, 'setSetting').mockResolvedValue({ ok: true })
    const matrix = await openNotifications()

    const on = matrix.querySelector<HTMLInputElement>(
      '.ui-toggle-matrix__cell[data-row="programNotify"][data-column="banner"] input',
    )!
    const off = matrix.querySelector<HTMLInputElement>(
      '.ui-toggle-matrix__cell[data-row="bell"][data-column="banner"] input',
    )!
    expect(on.checked).toBe(true)
    expect(off.checked).toBe(false)

    on.checked = false
    on.dispatchEvent(new Event('change', { bubbles: true }))
    await vi.waitFor(() => {
      expect(setSetting).toHaveBeenCalledWith('notifications.route.programNotify.banner', false)
    })
  })

  // A control that exists only inside a custom grid the search cannot see is
  // a control the user cannot find.
  it('the section is searchable, and searching narrows the matrix rather than emptying it', async () => {
    const matrix = await openNotifications()

    const searchInput = target.querySelector<HTMLInputElement>('input[type="search"]')!
    searchInput.value = 'In-app toast'
    searchInput.dispatchEvent(new Event('input', { bubbles: true }))

    await vi.waitFor(() => {
      expect(visibleColumnLabels(matrix)).toEqual(['In-app toast'])
    })
    // Both kinds carry a toast cell, so both rows stay.
    expect(visibleRowLabels(matrix)).toEqual(['Terminal bell', 'Program notification request'])
    // The number in the same section does not match and is hidden.
    const number = document.getElementById('st-setting-notifications.debounceMs')!
    expect(number.classList.contains('st-vis-hidden')).toBe(true)

    // Narrowing to one kind leaves one row and both of its columns.
    searchInput.value = 'Terminal bell'
    searchInput.dispatchEvent(new Event('input', { bubbles: true }))
    await vi.waitFor(() => {
      expect(visibleRowLabels(matrix)).toEqual(['Terminal bell'])
    })
    expect(visibleColumnLabels(matrix)).toEqual(['OS banner', 'In-app toast'])

    // A search that matches nothing in the section takes the whole grid away
    // rather than leaving a table of headers with no controls under them.
    //
    // It goes by being dropped from `visibleSections`, not by being marked
    // `st-vis-hidden` where it stands: a section with nothing showing is not
    // rendered at all (nocx-avogl.4 — the rows of a page nobody is on were
    // the tail of the scroller's content, so the scroll-chain proofs were
    // measuring a row nobody could see). The property this asserts is
    // unchanged and the mechanism is stronger, so the assertion follows it.
    searchInput.value = 'cursor'
    searchInput.dispatchEvent(new Event('input', { bubbles: true }))
    await vi.waitFor(() => {
      expect(target.querySelector('.ui-settings-matrix')).toBeNull()
    })
  })

  // A cell is addressable exactly as a row is: the deep link from a
  // notification, or from anything else that knows a key, has to land on the
  // control itself and not merely on the section holding it.
  it('a deep link lands on a cell and focuses its switch, search cleared', async () => {
    await openNotifications()

    const searchInput = target.querySelector<HTMLInputElement>('input[type="search"]')!
    searchInput.value = 'cursor'
    searchInput.dispatchEvent(new Event('input', { bubbles: true }))
    await vi.waitFor(() => {
      expect(target.querySelector('.ui-settings-matrix')).toBeNull()
    })

    content.scrollToKey('notifications.route.bell.toast')

    expect(searchInput.value).toBe('')
    const cell = document.getElementById('st-setting-notifications.route.bell.toast')!
    expect(cell.classList.contains('ui-settings-matrix-cell')).toBe(true)
    const control = cell.querySelector<HTMLElement>('input, select, button')
    expect(control).toBeTruthy()
    expect(document.activeElement).toBe(control)
  })

  it('a setting in the section that is not a cell keeps its own row', async () => {
    await openNotifications()
    const number = document.getElementById('st-setting-notifications.debounceMs')!
    expect(number.classList.contains('ui-settings-row')).toBe(true)
    expect(number.querySelector('input[type="number"]')).toBeTruthy()
  })

  // "Visible rather than silently absent from the grid": a key under the
  // namespace that the convention cannot parse is still a control the user
  // can operate, in the section it was declared in.
  it('a malformed cell key falls out of the grid as an ordinary, operable row', async () => {
    const malformed: Declaration = {
      key: 'notifications.route.bell',
      section: 'Notifications',
      label: 'Terminal bell',
      description: 'A key the cell convention cannot place.',
      control: 'toggle',
      dataClass: 'publicConfig',
      default: false,
    }
    await openNotifications([...NOTIFY_DECLARATIONS, malformed])

    const row = document.getElementById('st-setting-notifications.route.bell')!
    expect(row.classList.contains('ui-settings-row')).toBe(true)
    expect(row.querySelector('input[type="checkbox"]')).toBeTruthy()
    expect(row.closest('.ui-toggle-matrix')).toBeNull()
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
    client = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))
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

// ── The person's own paragraph (nocx-avogl.4, design §1 item 6) ────────
//
// Asserted as the task a person performs, not as what the component
// renders: they open Settings, find the field on the Assistant rail, type
// their paragraph into it, and it is written — no Save button anywhere near
// it. Plus the bound, which the criterion requires to be ON THE SCREEN
// rather than enforced silently.
describe("the person's own instructions to the assistant", () => {
  let target: HTMLDivElement
  let client: ProfileClient
  let content: SettingsContent
  let host: PaneHost
  let signal: AbortSignal

  const PERSONAL: Declaration = {
    key: 'assistant.personalInstructions',
    section: 'Instructions',
    label: 'Your instructions to the assistant',
    description: 'Standing instructions added to the end of every question you ask.',
    control: 'text',
    dataClass: 'privateContent',
    default: '',
    multiline: true,
    max: 2000,
    unit: 'characters',
  }

  beforeEach(() => {
    document.body.replaceChildren()
    target = document.createElement('div')
    document.body.append(target)
    client = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))
    content = new SettingsContent(client)
    host = mockPaneHost()
    signal = new AbortController().signal
  })

  async function openInstructions(): Promise<HTMLTextAreaElement> {
    mockReady(client, {
      declarations: [PERSONAL],
      sectionGroups: { Instructions: 'assistant' },
    })
    await content.mount(target, host, signal)
    // From the state a user starts in: the screen opens on Connections, so
    // the rail is what takes them to the field.
    openSection(target, 'Instructions')
    let area: HTMLTextAreaElement | null = null
    await vi.waitFor(() => {
      area = target.querySelector<HTMLTextAreaElement>('textarea.ui-text-field__input')
      expect(area).toBeTruthy()
    })
    return area!
  }

  it('typing a paragraph into it writes it, with no save step', async () => {
    const save = vi.spyOn(client, 'setSetting').mockResolvedValue({ ok: true })
    const area = await openInstructions()

    fireEvent.input(area, { target: { value: 'Never suggest brew. This machine uses nix.' } })

    await vi.waitFor(() => {
      expect(save).toHaveBeenCalledWith(
        'assistant.personalInstructions',
        'Never suggest brew. This machine uses nix.',
      )
    })
    // No Save button beside it: the row's only controls are the field and
    // the reset affordance every settings row carries.
    const row = document.getElementById('st-setting-assistant.personalInstructions')!
    expect(row.querySelectorAll('button').length).toBeLessThanOrEqual(1)
  })

  it('states its length bound on the screen, and says so before anything is lost to it', async () => {
    vi.spyOn(client, 'setSetting').mockResolvedValue({ ok: true })
    const area = await openInstructions()

    const caption = () =>
      document
        .getElementById('st-setting-assistant.personalInstructions')!
        .querySelector('.ui-text-field__caption')
    expect(caption()).toBeTruthy()
    expect(caption()!.textContent).toContain('2000')
    expect(caption()!.textContent).toContain('characters')

    fireEvent.input(area, { target: { value: 'x'.repeat(1990) } })
    await vi.waitFor(() => {
      expect(caption()!.textContent).toContain('1990')
    })
  })

  it('too long is refused visibly, in the field, rather than truncated', async () => {
    const save = vi.spyOn(client, 'setSetting').mockResolvedValue({ ok: true })
    const area = await openInstructions()

    fireEvent.input(area, { target: { value: 'y'.repeat(2001) } })

    await vi.waitFor(() => {
      const slot = document
        .getElementById('st-setting-assistant.personalInstructions')!
        .querySelector('.ui-text-field__caption[data-tone="error"]')
      expect(slot?.textContent).toBe('Must be at most 2000 characters')
    })
    // What the person typed is still on the screen, whole: the field never
    // shortens their text behind them.
    expect(area.value.length).toBe(2001)
    // And the backend's own rejection is not printed under a field whose
    // caption already says the same thing.
    expect(target.querySelector('.ui-settings-error')).toBeNull()
    expect(save).toHaveBeenCalled()
  })
  it('shows the full static nocx prompt above personal instructions with pane placeholders', async () => {
    const area = await openInstructions()
    const prompt = target.querySelector<HTMLElement>('.ui-code-block')
    const heading = Array.from(target.querySelectorAll('h2')).find(
      (element) => element.textContent === 'What the person added',
    )
    const row = document.getElementById('st-setting-assistant.personalInstructions')
    expect(prompt).toBeTruthy()
    expect(prompt!.getAttribute('aria-label')).toBe('nocx system prompt')
    expect(prompt!.getAttribute('tabindex')).toBe('0')
    // No session id, and its ABSENCE is the assertion (nocx-i4gg7). The
    // prompt used to state one and instruct the model to echo it into every
    // call, to carry a fact the backend already held; the session is now
    // supplied by the backend and there is no placeholder to preview.
    expect(prompt!.textContent).not.toContain('<session id>')
    expect(prompt!.textContent).not.toContain('Session id')
    expect(prompt!.textContent).toContain('<working directory>')
    expect(prompt!.textContent).toContain('<local shell or ssh session>')
    expect(prompt!.textContent).toContain('<host or local machine>')
    expect(prompt!.textContent).toContain('<attached or absent>')
    for (const intakeRule of [
      'A link on its own means go there and tell the person what is on it.',
      'When the intent is not plain, ask one question and stop.',
      // The settings artifact shows the prompt WITH attachments, and there
      // the rule carries its exemption: what came with the question is not
      // an outside check (nocx-hp8p2.4).
      'Do not guess, and do not go outside this pane to check first',
      'what is attached above is already yours',
    ]) {
      expect(systemPromptText).toContain(intakeRule)
      expect(prompt!.textContent).toContain(intakeRule)
    }
    const noteRule = systemPromptText.match(/Text on its own[^]*?\. /)?.[0]
    expect(noteRule).toBeTruthy()
    expect(noteRule).toContain('notes.create')
    expect(prompt!.textContent).toContain(noteRule!)
    expect(prompt!.textContent).not.toContain('s-real-session')
    expect(prompt!.textContent).not.toContain('/home/real-user/project')
    expect(prompt!.textContent).not.toContain('real-host.example')
    expect(prompt!.textContent).not.toContain('What the person added')
    expect(row).toBeTruthy()
    expect(heading).toBeTruthy()
    expect(row!.textContent).toContain('Your instructions to the assistant')
    expect(prompt!.compareDocumentPosition(row!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(
      prompt!.compareDocumentPosition(heading!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(heading!.compareDocumentPosition(row!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(prompt!.compareDocumentPosition(area) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('keeps the long prompt in the kit scroll container so the personal field remains reachable', async () => {
    await openInstructions()
    const wrap = target.querySelector<HTMLElement>('.ui-code-block-wrap')
    const prompt = target.querySelector<HTMLElement>('.ui-code-block')
    const row = document.getElementById('st-setting-assistant.personalInstructions')

    expect(wrap).toBeTruthy()
    expect(prompt).toBeTruthy()
    expect(prompt!.parentElement).toBe(wrap)
    expect(prompt!.getAttribute('tabindex')).toBe('0')
    expect(row).toBeTruthy()
    expect(prompt!.compareDocumentPosition(row!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const codeBlockCss = readFileSync(
      resolve(import.meta.dirname ?? '.', 'styles/components/code-block.css'),
      'utf8',
    )
    expect(codeBlockCss).toMatch(
      /\.ui-code-block\s*\{[\s\S]*max-height:\s*200px;[\s\S]*overflow:\s*auto;/,
    )
  })
})

// ── The value the field was holding reaches the create form (nocx-3o0ed.6) ──
//
// The no-mint-seam fallback: a person presses the lock over a filled value
// field in a workbench with no vault client of its own, the picker offers to
// store what they are holding, and the create ask is the Secrets page. Before
// this, only the NAME travelled — they landed on an empty value field and had
// to go back and copy the value out by hand, which is exactly the detour this
// epic exists to remove.
//
// Driven from `createVaultSecretSource`, which IS the source main.tsx wires
// (main.tsx builds no second one), through SettingsContent.startNewSecret and
// the Solid handle, to the form a person actually reads.
describe('the Secrets fallback carries the value, not only the name', () => {
  let target: HTMLDivElement
  let client: ProfileClient
  let content: SettingsContent
  let host: PaneHost
  let signal: AbortSignal
  let vaultController: VaultController
  let vaultClient: VaultClient

  const UNSEALED = {
    state: 'unsealed' as const,
    hasPassphrase: true,
    autoSealMinutes: 15,
    providers: [{ id: 'system', writable: true, ready: true, reason: undefined }],
    defaultProvider: 'system',
  }

  beforeEach(() => {
    document.body.replaceChildren()
    toasts.length = 0
    target = document.createElement('div')
    document.body.append(target)
    client = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))
    vaultController = {
      status: () => UNSEALED,
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
    vaultClient = {
      status: vi.fn().mockResolvedValue(UNSEALED),
      inventory: vi.fn().mockResolvedValue({ entries: [] }),
      setup: vi.fn(),
      unseal: vi.fn(),
      seal: vi.fn(),
      changePassphrase: vi.fn(),
      regenerateRecovery: vi.fn(),
      setDefaultProvider: vi.fn(),
      setAutoSeal: vi.fn().mockResolvedValue(undefined),
      activity: vi.fn(),
    } as unknown as VaultClient
    content = new SettingsContent(client, undefined, vaultController, vaultClient)
    host = mockPaneHost()
    signal = new AbortController().signal
  })

  /** The picker source main.tsx wires, with the Settings pane already open. */
  function fallbackSource() {
    return createVaultSecretSource({
      vaultClient,
      vaultController,
      openSecretCreate: (name, value) => content.startNewSecret(name, value),
    })
  }

  async function addDialog(): Promise<{ name: HTMLInputElement; value: HTMLInputElement }> {
    let name: HTMLInputElement | null = null
    let value: HTMLInputElement | null = null
    await vi.waitFor(() => {
      name = document.querySelector<HTMLInputElement>('#sr-add-name')
      value = document.querySelector<HTMLInputElement>('#sr-add-value')
      expect(name).toBeTruthy()
      expect(value).toBeTruthy()
    })
    return { name: name!, value: value! }
  }

  it('the store offer lands in the create form with the value already filled in', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    await fallbackSource().requestCreate('', 'Bearer t.Yixxxx')

    const { name, value } = await addDialog()
    expect(value.value).toBe('Bearer t.Yixxxx')
    expect(name.value).toBe('')
  })

  it('a name typed after @ AND a value both arrive', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    await fallbackSource().requestCreate('api-token', 'Bearer t.Yixxxx')

    const { name, value } = await addDialog()
    expect(name.value).toBe('api-token')
    expect(value.value).toBe('Bearer t.Yixxxx')
  })

  it('the create row with no value opens the form on an empty value field', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    await fallbackSource().requestCreate('api-token')

    const { name, value } = await addDialog()
    expect(name.value).toBe('api-token')
    expect(value.value).toBe('')
  })

  // The other callers hand a name alone (main.tsx: tm.onCreateSecret, the
  // quick-connect secrets provider). They must keep working, and the value
  // they do not have must not arrive as anything but an empty field.
  it('startNewSecret with a name alone still opens the form', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    content.startNewSecret('deploy-key')

    const { name, value } = await addDialog()
    expect(name.value).toBe('deploy-key')
    expect(value.value).toBe('')
  })

  // Queued before mount: opening Settings and asking for the form is one user
  // action, and the mount is a promise the caller does not hold. The value has
  // to survive that queue or the fallback loses it exactly when the tab was
  // freshly opened — the common case.
  it('a value asked for before the tab mounts survives the queue', async () => {
    mockReady(client)

    content.startNewSecret('api-token', 'Bearer t.Yixxxx')
    await content.mount(target, host, signal)

    const { name, value } = await addDialog()
    expect(name.value).toBe('api-token')
    expect(value.value).toBe('Bearer t.Yixxxx')
  })

  it('nothing inspects the value: a second create replaces it whole', async () => {
    mockReady(client)
    await content.mount(target, host, signal)

    await fallbackSource().requestCreate('first', 'AKIA0000000000000000')
    await addDialog()
    await fallbackSource().requestCreate('second', '-----BEGIN OPENSSH PRIVATE KEY-----')

    await vi.waitFor(() => {
      const { name, value } = {
        name: document.querySelector<HTMLInputElement>('#sr-add-name')!,
        value: document.querySelector<HTMLInputElement>('#sr-add-value')!,
      }
      expect(name.value).toBe('second')
      expect(value.value).toBe('-----BEGIN OPENSSH PRIVATE KEY-----')
    })
  })
})
