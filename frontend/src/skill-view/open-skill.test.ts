// @vitest-environment jsdom
//
// openSkill tests: the namespaced singleton key (openPane matches its key
// ALONE, panes.ts:1630), the registry factory's refusal to build a view with
// no skill, the re-read on every `setVisible(true)` (main.tsx:238 — no
// change notification on the wire, a writer re-reads), the tab closing when
// the skill it is about leaves the list (skills-section.tsx:265's Dialog did
// this; deleting the Dialog must not delete the behaviour) — including the
// same-name-different-root case that is the actual point of matching by
// path — and the one wired control, the enable switch. A real PaneManager is
// used, as open-file-viewer.test.ts's is — the dedup lives in
// PaneManager.openPane, and asserting it through a fake would test the fake
// instead.
import { describe, expect, it, vi, type Mock } from 'vitest'
import { mountPaneManager } from '../test-support/panes-fixtures'
import { SurfaceRegistry, SURFACE_ID_SKILL } from '../surface-registry'
import { SkillsStore, type SkillsClientLike } from '../skills-store'
import type { SkillsList } from '../generated/skills.list'
import { registerSkillSurface, openSkill } from './index'

// The toast host is mounted by App.tsx, which this suite does not mount —
// the same reason panes-displaced.test.ts mocks it — so a failed toggle's
// toast is asserted where it is raised rather than by looking for DOM this
// test never built.
const showToastMock = vi.fn()
vi.mock('../ui/toast', () => ({
  showToast: (...args: unknown[]) => {
    showToastMock(...args)
  },
}))

// jsdom lacks matchMedia, which the terminal's mount path touches during
// initial-tab startup — mountPaneManager opens one terminal tab before this
// suite ever calls openSkill (see renderers/xterm.test.ts and
// open-file-viewer.test.ts for the same stub).
window.matchMedia = (query: string) =>
  ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }) as MediaQueryList

const A_SKILL: SkillsList['skills'][number] = {
  name: 'deploy',
  description: 'Deploy the service',
  provenance: 'authored',
  path: '/tmp/nocx/skills/deploy/SKILL.md',
  enabled: true,
  status: 'approved',
  usage: { count: 0 },
  pins: {},
}

function fakeClient(overrides: Partial<SkillsClientLike> = {}): SkillsClientLike {
  return {
    setPin: vi.fn().mockResolvedValue({ name: A_SKILL.name, pins: {} }),
    list: vi.fn().mockResolvedValue({ documentPath: '/tmp/nocx/skills.json', skills: [A_SKILL] }),
    setEnabled: vi.fn().mockResolvedValue({ name: A_SKILL.name, enabled: false }),
    remove: vi.fn().mockResolvedValue({ name: A_SKILL.name }),
    approve: vi.fn().mockResolvedValue({ name: A_SKILL.name, status: 'approved' }),
    file: vi.fn().mockRejectedValue(new Error('not asked for in this suite')),
    files: vi.fn().mockRejectedValue(new Error('not asked for in this suite')),
    scan: vi.fn().mockRejectedValue(new Error('not asked for in this suite')),
    audit: vi.fn().mockRejectedValue(new Error('not asked for in this suite')),
    check: vi.fn().mockResolvedValue({ name: A_SKILL.name, checked: false }),
    ...overrides,
  }
}

async function setup(client: SkillsClientLike = fakeClient()) {
  const { manager, bar } = await mountPaneManager()
  const registry = new SurfaceRegistry()
  const store = new SkillsStore(client)
  registerSkillSurface(registry, manager, { store })
  const openPane = vi.spyOn(manager, 'openPane')
  return { manager, bar, registry, store, client, openPane }
}

function titles(bar: HTMLElement): string[] {
  return Array.from(bar.querySelectorAll('.nocx-tab-title')).map((el) => el.textContent ?? '')
}

/** Open a skill (`deploy` unless told otherwise) and let the activation's
 *  synchronous setVisible(true) and the async mount + subscribe both settle,
 *  returning the Pane's own DOM element — the seam every test below reaches
 *  the mounted switch, and now the header's facts, through. Indexed off the
 *  END of `results` rather than `[0]`, so a test opening a skill other than
 *  the suite's first still finds its own pane. */
async function openAndSettle(
  openPane: { mock: { results: { value: unknown }[] } },
  name = 'deploy',
): Promise<HTMLElement> {
  openSkill(name)
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
  const results = openPane.mock.results
  const pane = results[results.length - 1]?.value as { pane: HTMLElement }
  return pane.pane
}

function switchInput(paneEl: HTMLElement): HTMLInputElement {
  const input = paneEl.querySelector<HTMLInputElement>('input[type="checkbox"]')
  if (!input) throw new Error('the enable switch did not render')
  return input
}

/** The header's own FactList, by the name a person reads on each row — the
 *  same helper the modal card's tests used to read `cardFacts` with
 *  (skills-section.test.tsx's `recordFactsIn`, nocx-54a2c). The value cell
 *  carries any qualifying note inside it, so a caveat is read with
 *  `toContain` rather than equality — a caveat lives ON the row it
 *  qualifies (FactList). */
function recordFactsIn(paneEl: HTMLElement): Record<string, string> {
  const list = paneEl.querySelector('[aria-label="Where this skill lives"]')
  const facts: Record<string, string> = {}
  for (const row of Array.from(list?.querySelectorAll('.ui-fact-list__row') ?? [])) {
    const name = row.querySelector('.ui-fact-list__name')?.textContent?.trim() ?? ''
    facts[name] = row.querySelector('.ui-fact-list__value')?.textContent?.trim() ?? ''
  }
  return facts
}

/** An installed skill with a full source record — the address, when the
 *  bytes were taken, and the digest of what that address served. */
const INSTALLED_SKILL: SkillsList['skills'][number] = {
  name: 'weather',
  description: 'Answer questions about the weather',
  provenance: 'installed',
  path: '/tmp/nocx/installed-skills/weather/SKILL.md',
  enabled: false,
  status: 'approved',
  usage: { count: 0 },
  pins: {},
  source: {
    url: 'https://example.com/weather/SKILL.md',
    installedAt: '2026-09-03T12:00:00Z',
    digest: 'c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00',
  },
}

describe('openSkill — the tab a skill is read in', () => {
  it('namespaces its singleton key, because openPane matches the key alone', async () => {
    const { openPane } = await setup()

    openSkill('deploy')

    expect(openPane).toHaveBeenCalledWith(
      expect.anything(),
      expect.objectContaining({ singletonKey: 'skill:deploy', defaultTitle: 'deploy' }),
    )
  })

  it('opens one tab per skill, however many times it is asked', async () => {
    const { openPane, bar } = await setup()

    openSkill('deploy')
    await Promise.resolve()
    openSkill('deploy')
    await Promise.resolve()

    expect(titles(bar).filter((t) => t === 'deploy')).toHaveLength(1)
    expect(openPane).toHaveBeenCalledTimes(2)
    // The second open activated the existing tab: openPane's own dedup
    // (singletonKey), not this seam, is what returns the SAME pane both
    // times — asserted on the real return value rather than through a fake
    // that could not tell one call from a collapsed one.
    const results = openPane.mock.results as { value: unknown }[]
    expect(results[0]?.value).toBe(results[1]?.value)
  })

  it('refuses to build a skill view with no skill', async () => {
    const { registry } = await setup()

    expect(() => registry.get(SURFACE_ID_SKILL)!.factory()).toThrow(
      /cannot be opened without a skill/,
    )
  })

  it('re-reads the store when it becomes visible again', async () => {
    const { openPane, client } = await setup()

    await openAndSettle(openPane)

    const before = (client.list as Mock).mock.calls.length
    expect(before).toBeGreaterThan(0)

    const pane = openPane.mock.results[0]?.value as { content: { setVisible(v: boolean): void } }
    pane.content.setVisible(false)
    pane.content.setVisible(true)
    await Promise.resolve()

    expect((client.list as Mock).mock.calls.length).toBeGreaterThan(before)
  })

  it('closes the tab when the skill it is about is deleted outright', async () => {
    const client = fakeClient()
    const { bar, store, openPane } = await setup(client)

    await openAndSettle(openPane)
    expect(titles(bar)).toContain('deploy')

    ;(client.list as Mock).mockResolvedValue({
      documentPath: '/tmp/nocx/skills.json',
      skills: [],
    })
    await store.refresh()
    await Promise.resolve()
    await Promise.resolve()

    expect(titles(bar)).not.toContain('deploy')
  })

  it('closes the tab when a same-named skill from a different root takes over', async () => {
    // NOT an empty list: an empty list only exercises the bare `!found`
    // branch. A skill of the SAME NAME from a DIFFERENT PATH is what
    // actually reaches the `skill.path === this.resolvedPath` condition in
    // skill-view-content.tsx's onStoreState — deleting that condition (and
    // matching by name alone) would leave the "deleted outright" test above
    // green while silently re-pointing this tab at bytes nobody asked for.
    const client = fakeClient()
    const { bar, store, openPane } = await setup(client)

    await openAndSettle(openPane)
    expect(titles(bar)).toContain('deploy')

    ;(client.list as Mock).mockResolvedValue({
      documentPath: '/tmp/nocx/skills.json',
      skills: [{ ...A_SKILL, path: '/other/lower-precedence-root/deploy/SKILL.md' }],
    })
    await store.refresh()
    await Promise.resolve()
    await Promise.resolve()

    expect(titles(bar)).not.toContain('deploy')
  })

  it('toggling the switch calls setEnabled with the RESOLVED skill name', async () => {
    const { openPane, client } = await setup()

    const paneEl = await openAndSettle(openPane)
    const input = switchInput(paneEl)
    expect(input.checked).toBe(true) // A_SKILL.enabled

    input.checked = false
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await Promise.resolve()

    // client.setEnabled is a vi.fn() on a plain fake object, never a
    // bound-`this` method; the rule cannot tell that from SkillsClientLike's
    // interface signature.
    // eslint-disable-next-line @typescript-eslint/unbound-method
    expect(client.setEnabled).toHaveBeenCalledWith('deploy', false)
  })

  it('toggling a pin calls skills.setPin and refreshes the skill', async () => {
    const setPin = vi.fn().mockResolvedValue({ name: A_SKILL.name, pins: { keepEnabled: true } })
    const client = fakeClient({ setPin })
    const { openPane } = await setup(client)
    const paneEl = await openAndSettle(openPane)
    const pin = Array.from(paneEl.querySelectorAll<HTMLLabelElement>('.ui-checkbox')).find(
      (label) => label.textContent?.includes('Keep enabled when unused'),
    )
    const input = pin?.querySelector<HTMLInputElement>('input')
    if (!input) throw new Error('keep-enabled pin did not render')
    input.checked = true
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()

    expect(setPin).toHaveBeenCalledWith('deploy', 'keepEnabled', true)
    // eslint-disable-next-line @typescript-eslint/unbound-method
    expect(client.list).toHaveBeenCalledTimes(2)
  })

  it('a failed toggle shows a toast and leaves the switch usable again', async () => {
    showToastMock.mockClear()
    const client = fakeClient({ setEnabled: vi.fn().mockRejectedValue(new Error('disk is full')) })
    const { openPane } = await setup(client)

    const paneEl = await openAndSettle(openPane)
    const input = switchInput(paneEl)

    input.checked = false
    input.dispatchEvent(new Event('change', { bubbles: true }))
    // The rejection and the finally both need a turn of the microtask queue.
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()

    expect(showToastMock).toHaveBeenCalledWith(
      expect.objectContaining({
        level: 'danger',
        message: expect.stringContaining('disk is full') as string,
      }),
    )
    // busy is cleared in the `finally`: a failure leaves the switch pressable
    // again rather than stuck disabled with no way to retry.
    expect(input.disabled).toBe(false)
  })

  // Moved from the modal card's own tests (skills-section.test.tsx's
  // "SkillsSection — the skill's card", nocx-ojfuc.3, nocx-54a2c): the
  // record used to be readable only by opening skills.json by hand, or by
  // opening the card the row's Open button used to show. It is the tab's
  // header now — the address, when the bytes were taken, and what that
  // address served, beside the path every skill already carries.
  it('reads the whole record of what an installed skill resolved to', async () => {
    const client = fakeClient({
      list: vi.fn().mockResolvedValue({
        documentPath: '/tmp/nocx/skills.json',
        skills: [INSTALLED_SKILL],
      }),
    })
    const { openPane } = await setup(client)
    const paneEl = await openAndSettle(openPane, 'weather')
    const facts = recordFactsIn(paneEl)

    expect(facts['Where it is']).toBe('/tmp/nocx/installed-skills/weather/SKILL.md')
    expect(facts['Installed from']).toBe('https://example.com/weather/SKILL.md')
    // The moment, in the reader's own locale — a record is read months
    // later, where "312 d ago" is the form that makes them do arithmetic.
    expect(facts['Taken on']).toBe(new Date('2026-09-03T12:00:00Z').toLocaleString())
    // The digest, with its qualification ON its row: a hash of bytes a
    // stranger served is change detection and never a vouch for them.
    expect(facts['What that address served']).toContain(
      'c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00',
    )
    expect(facts['What that address served']).toContain('not a verdict')

    // AND NOTHING ABOUT HOW IT WAS FOUND. The search, the page the model
    // read and the links it followed are not recorded anywhere, so there is
    // no row here implying they were — an agent's route is not reproducible,
    // and a record of one would read like evidence and function as a story.
    expect(paneEl.textContent).not.toContain('Found via')
    expect(paneEl.textContent).not.toContain('Searched')
    expect(paneEl.textContent).not.toContain('Repository')
  })

  it('says nothing about a digest for a source recorded before one was', async () => {
    // The row a purely additive schema step leaves behind: an address and a
    // time, and no claim about what that address served. Absent is "nothing
    // was recorded" and must not render as an empty value or a zero hash.
    const client = fakeClient({
      list: vi.fn().mockResolvedValue({
        documentPath: '/tmp/nocx/skills.json',
        skills: [
          {
            ...INSTALLED_SKILL,
            source: {
              url: INSTALLED_SKILL.source!.url,
              installedAt: INSTALLED_SKILL.source!.installedAt,
            },
          },
        ],
      }),
    })
    const { openPane } = await setup(client)
    const paneEl = await openAndSettle(openPane, 'weather')
    const facts = recordFactsIn(paneEl)

    expect(facts['Installed from']).toBe('https://example.com/weather/SKILL.md')
    expect(facts['Taken on']).toBe(new Date('2026-09-03T12:00:00Z').toLocaleString())
    expect('What that address served' in facts).toBe(false)
  })

  it('draws no part of the record for a skill nothing was recorded about', async () => {
    // A directory somebody moved into the installed root by hand: installed
    // provenance, no source row. The header still says where the file is —
    // the fact every skill has — and says nothing false about the rest by
    // saying nothing at all.
    const client = fakeClient({
      list: vi.fn().mockResolvedValue({
        documentPath: '/tmp/nocx/skills.json',
        skills: [{ ...INSTALLED_SKILL, source: undefined }],
      }),
    })
    const { openPane } = await setup(client)
    const paneEl = await openAndSettle(openPane, 'weather')
    const facts = recordFactsIn(paneEl)

    expect(facts['Where it is']).toBe('/tmp/nocx/installed-skills/weather/SKILL.md')
    expect(Object.keys(facts)).toEqual(['Where it is'])
  })

  // Moved from skills-section.test.tsx's "persists a toggle through the
  // store and refreshes the returned state" (nocx-54a2c): the switch must
  // read the STORE's answer and not a local optimistic flip — a person
  // relying on this control to know whether the assistant is offered a
  // skill is relying on it to reflect what actually landed.
  it('reflects the store state after a toggle round-trips, not a local guess', async () => {
    // The store answers TRUE — as though the write were refused or reverted
    // — deliberately DIFFERENT from what the click set by hand (review
    // finding, minor 1): with Solid's fine-grained binding, a signal that
    // does not change fires no effect, so answering the SAME value the
    // click already set would leave the DOM looking right whether or not
    // the refresh landed at all, or was even read. Only a value that moves
    // the DOM away from the click proves the switch reads the store rather
    // than its own write.
    const setEnabled = vi.fn().mockResolvedValue({ name: A_SKILL.name, enabled: false })
    const client = fakeClient({
      setEnabled,
      list: vi
        .fn()
        .mockResolvedValueOnce({ documentPath: '/tmp/nocx/skills.json', skills: [A_SKILL] })
        .mockResolvedValue({
          documentPath: '/tmp/nocx/skills.json',
          skills: [{ ...A_SKILL, enabled: true }],
        }),
    })
    const { openPane } = await setup(client)
    const paneEl = await openAndSettle(openPane)
    const input = switchInput(paneEl)
    expect(input.checked).toBe(true)

    input.checked = false
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()

    expect(setEnabled).toHaveBeenCalledWith('deploy', false)
    // The DOM reflects what the STORE answered on refresh (still true) —
    // not the false the click set by hand a moment before.
    expect(input.checked).toBe(true)
  })

  // Restored from the deleted modal card's "shows a changed managed skill
  // with its path and offers re-approval" (review's Critical finding,
  // nocx-54a2c): this tab is now the surface a person reads a skill's bytes
  // and decides from, and `Skill.Offered()` (internal/skill/skill.go) is
  // refusing a changed skill whatever the switch says — the switch alone
  // cannot say that, so the header must.
  it('says the bytes changed, with a Re-approve action, for a skill whose status is changed', async () => {
    const approve = vi.fn().mockResolvedValue({ name: 'deploy', status: 'approved' })
    const changed: SkillsList = {
      refused: [],
      documentPath: '/tmp/nocx/skills.json',
      skills: [{ ...A_SKILL, status: 'changed' }],
    }
    const client = fakeClient({ approve, list: vi.fn().mockResolvedValue(changed) })
    const { openPane } = await setup(client)
    const paneEl = await openAndSettle(openPane)

    expect(paneEl.textContent).toContain('The bytes under this skill have changed')
    const reapprove = Array.from(paneEl.querySelectorAll<HTMLButtonElement>('button')).find(
      (button) => button.textContent?.trim() === 'Re-approve',
    )
    if (!reapprove) throw new Error('Re-approve did not render')

    reapprove.click()
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()

    expect(approve).toHaveBeenCalledWith('deploy')
  })

  it('says nothing about changed bytes for a skill whose status is approved', async () => {
    const { openPane } = await setup() // A_SKILL's own status is 'approved'
    const paneEl = await openAndSettle(openPane)

    expect(paneEl.textContent).not.toContain('bytes under this skill have changed')
    expect(
      Array.from(paneEl.querySelectorAll<HTMLButtonElement>('button')).some(
        (button) => button.textContent?.trim() === 'Re-approve',
      ),
    ).toBe(false)
  })

  // The tab offers a look and three immediate decisions: offer the skill,
  // keep it enabled when unused, and keep it unchanged by the assistant.
  it('offers the enable switch and the two machine pins on the skill tab', async () => {
    const { openPane } = await setup()
    const paneEl = await openAndSettle(openPane)

    const boxes = Array.from(paneEl.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
    expect(boxes).toHaveLength(3)
    expect(boxes.every((box) => box.getAttribute('role') === 'switch')).toBe(true)
    expect(paneEl.textContent).toContain('Keep enabled when unused')
    expect(paneEl.textContent).toContain('Keep unchanged by the assistant')
    expect(paneEl.textContent).not.toContain('I have')
  })
})
