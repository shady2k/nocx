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
}

function fakeClient(overrides: Partial<SkillsClientLike> = {}): SkillsClientLike {
  return {
    list: vi.fn().mockResolvedValue({ documentPath: '/tmp/nocx/skills.json', skills: [A_SKILL] }),
    setEnabled: vi.fn().mockResolvedValue({ name: A_SKILL.name, enabled: false }),
    remove: vi.fn().mockResolvedValue({ name: A_SKILL.name }),
    approve: vi.fn().mockResolvedValue({ name: A_SKILL.name, status: 'approved' }),
    file: vi.fn().mockRejectedValue(new Error('not asked for in this suite')),
    files: vi.fn().mockRejectedValue(new Error('not asked for in this suite')),
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

/** Open 'deploy' and let the activation's synchronous setVisible(true) and
 *  the async mount + subscribe both settle, returning the Pane's own DOM
 *  element — the seam every test below reaches the mounted switch through. */
async function openAndSettle(openPane: {
  mock: { results: { value: unknown }[] }
}): Promise<HTMLElement> {
  openSkill('deploy')
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
  const pane = openPane.mock.results[0]?.value as { pane: HTMLElement }
  return pane.pane
}

function switchInput(paneEl: HTMLElement): HTMLInputElement {
  const input = paneEl.querySelector<HTMLInputElement>('input[type="checkbox"]')
  if (!input) throw new Error('the enable switch did not render')
  return input
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
})
