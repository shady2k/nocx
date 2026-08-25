// @vitest-environment jsdom
//
// "Durable history is not running" — from the wire to the sentence on the
// screen (nocx-rtg0.15).
//
// The defect these are written against: the app already knew. It logged a
// slog.Warn and carried on, and the Settings History section went on
// offering a keep-history toggle, a retention age and a two-number budget,
// none of which governed anything. So the tests that matter here are the DOM
// ones at the bottom — they mount the real Settings surface and read what a
// user reads. The unit tests above them exist to pin the wording and the
// raise/clear behaviour that surface depends on.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import {
  HistoryStatusStore,
  historyDiscardSentence,
  historyUnavailableSentence,
  HISTORY_UNAVAILABLE_RECALL_TITLE,
} from './history-status'
import type { HistoryStatus } from './generated/history.status'
import type { WSClient } from './ipc'
import { SettingsContent } from './settings-content'
import { ProfileClient } from './profiles'
import { Dispatcher } from './dispatcher'
import type { PaneHost } from './pane-content'
import type { Declaration, SettingsGroup } from './settings-domain'

// ── A client that answers history.status and can push a change ───────────

interface FakeClient {
  client: WSClient
  /** Push a history.statusChanged notification, as the backend does. */
  push: (params: unknown) => void
  /** Fire the dispatcher's connect hook, as a reconnect does. */
  reconnect: () => void
  calls: string[]
  answer: HistoryStatus | Error
}

function fakeClient(initial: HistoryStatus | Error): FakeClient {
  const notifiers: ((params: unknown) => void)[] = []
  const connectors: (() => void)[] = []
  const state: FakeClient = {
    calls: [],
    answer: initial,
    push: (params) => notifiers.forEach((fn) => fn(params)),
    reconnect: () => connectors.forEach((fn) => fn()),
    client: {
      dispatcher: {
        subscribe: (method: string, fn: (params: unknown) => void) => {
          if (method === 'history.statusChanged') notifiers.push(fn)
          return () => {
            const i = notifiers.indexOf(fn)
            if (i >= 0) notifiers.splice(i, 1)
          }
        },
        onConnect: (fn: () => void) => {
          connectors.push(fn)
          return () => {
            const i = connectors.indexOf(fn)
            if (i >= 0) connectors.splice(i, 1)
          }
        },
      },
      call: (method: string) => {
        state.calls.push(method)
        return state.answer instanceof Error
          ? Promise.reject(state.answer)
          : Promise.resolve(state.answer)
      },
    } as unknown as WSClient,
  }
  return state
}

/** THE factory for a status in these tests. One, because a literal here is a
 *  promise that ages badly: HistoryStatus gains fields as the product learns
 *  new things to say about durable history — `discarded` was the first — and
 *  every literal keeps compiling with whatever the old shape had, so the test
 *  goes on passing over a status the backend no longer sends. A caller names
 *  only what its assertion is about. */
function aStatus(over: Partial<HistoryStatus> = {}): HistoryStatus {
  return { available: true, reason: null, detail: null, discarded: null, ...over }
}

const AVAILABLE = aStatus()
const NO_KEY = aStatus({
  reason: 'noKey',
  available: false,
  detail: 'contentkey: open salt: is a directory',
})

// ── The sentence ─────────────────────────────────────────────────────────

describe('historyUnavailableSentence', () => {
  it('says nothing about a status that is running', () => {
    expect(historyUnavailableSentence(AVAILABLE)).toBeNull()
  })

  it('says nothing before the status has been read', () => {
    // A surface shows its placeholder rather than a lie in either
    // direction: claiming a degrade we have not been told about is the
    // same defect as hiding one.
    expect(historyUnavailableSentence(null)).toBeNull()
  })

  it('names the state in the terms the controls under it are written in', () => {
    const s = historyUnavailableSentence(NO_KEY)
    expect(s?.title).toBe('Commands are not being kept')
  })

  it('gives each reason its own why, and carries the detail', () => {
    expect(historyUnavailableSentence(NO_KEY)?.description).toContain('key that encrypts')
    expect(historyUnavailableSentence(NO_KEY)?.description).toContain(
      'contentkey: open salt: is a directory',
    )
    expect(
      historyUnavailableSentence(aStatus({ available: false, reason: 'invalidBudget' }))
        ?.description,
    ).toContain('size limits')
    expect(
      historyUnavailableSentence(aStatus({ available: false, reason: 'openFailed' }))?.description,
    ).toContain('could not be opened')
  })

  it('still says something for a reason this build does not know', () => {
    // A newer backend, or a degrade raised without a reason. Saying less is
    // honest; saying nothing would leave the settings in charge of a
    // feature that is down.
    const s = historyUnavailableSentence(aStatus({ available: false }))
    expect(s).not.toBeNull()
    expect(s?.description).toContain('Nothing is being stored')
  })

  it('gives recall its own words for the same fact', () => {
    // Same fact, one owner, two audiences: in Settings the reader is looking
    // at the controls this contradicts; in recall they pressed Up.
    expect(HISTORY_UNAVAILABLE_RECALL_TITLE).not.toBe('')
  })
})

// ── The store ────────────────────────────────────────────────────────────

describe('HistoryStatusStore', () => {
  it('reads the status on start', async () => {
    const f = fakeClient(NO_KEY)
    const store = new HistoryStatusStore(f.client)
    expect(store.status()).toBeNull()
    store.start()
    await Promise.resolve()
    expect(f.calls).toEqual(['history.status'])
    expect(store.status()).toEqual(NO_KEY)
  })

  it('takes a raise pushed while the app is running', async () => {
    const f = fakeClient(AVAILABLE)
    const store = new HistoryStatusStore(f.client)
    const seen: (HistoryStatus | null)[] = []
    store.subscribe((s) => seen.push(s))
    store.start()
    await Promise.resolve()

    f.push(NO_KEY)
    expect(store.status()).toEqual(NO_KEY)
    // The clear closes the episode, and the surface hears about it.
    f.push(AVAILABLE)
    expect(store.status()).toEqual(AVAILABLE)
    expect(seen).toEqual([AVAILABLE, NO_KEY, AVAILABLE])
  })

  it('does not fire for a re-read that changed nothing', async () => {
    const f = fakeClient(NO_KEY)
    const store = new HistoryStatusStore(f.client)
    store.start()
    await Promise.resolve()
    const seen: (HistoryStatus | null)[] = []
    store.subscribe((s) => seen.push(s))

    // A fresh object with the same values — every read mints one.
    f.push({ ...NO_KEY })
    expect(seen).toEqual([])
  })

  it('re-reads on reconnect: the backend keeps this state in memory', async () => {
    const f = fakeClient(AVAILABLE)
    const store = new HistoryStatusStore(f.client)
    store.start()
    await Promise.resolve()
    expect(store.status()).toEqual(AVAILABLE)

    f.answer = NO_KEY
    f.reconnect()
    await Promise.resolve()
    await Promise.resolve()
    expect(store.status()).toEqual(NO_KEY)
  })

  it('a failed read leaves the last known status alone', async () => {
    const f = fakeClient(NO_KEY)
    const store = new HistoryStatusStore(f.client)
    store.start()
    await Promise.resolve()
    expect(store.status()).toEqual(NO_KEY)

    f.answer = new Error('socket closed')
    await store.refresh()
    // An unanswered question is not an answer. Flipping to "available"
    // because a socket hiccuped is the same class of lie as hiding a real
    // degrade.
    expect(store.status()).toEqual(NO_KEY)
  })

  it('a malformed push is dropped, not rendered', async () => {
    const f = fakeClient(NO_KEY)
    const store = new HistoryStatusStore(f.client)
    store.start()
    await Promise.resolve()
    f.push({ nonsense: true })
    expect(store.status()).toBeNull()
  })

  it('stops listening after stop()', async () => {
    const f = fakeClient(AVAILABLE)
    const store = new HistoryStatusStore(f.client)
    store.start()
    await Promise.resolve()
    store.stop()
    f.push(NO_KEY)
    expect(store.status()).toEqual(AVAILABLE)
  })
})

// ── The screen ───────────────────────────────────────────────────────────
//
// The acceptance criterion, driven end to end: the History section says so
// where the user is looking. Asserted on rendered text, never on the flag.

const HISTORY_DECLS: Declaration[] = [
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
]
// The whole rail catalogue, mirroring what the Go side declares. It has to
// be whole: the component pages (Secrets, Vault, Endpoints, Snippets) are
// registered unconditionally, and GroupedRail refuses an item naming a group
// the catalogue does not declare — a fixture missing one fails the load
// rather than the assertion, which is a slow way to learn it.
const GROUPS: SettingsGroup[] = [
  { id: 'assistant', title: 'Assistant', order: 0 },
  { id: 'vault', title: 'Vault', order: 1 },
  { id: 'application', title: 'Application', order: 2 },
  { id: 'developer', title: 'Developer', order: 3 },
]

// jsdom implements neither, and the settings Page reaches for both.
if (!('scrollIntoView' in HTMLElement.prototype)) {
  Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {
    value: vi.fn(),
    writable: true,
    configurable: true,
  })
}
if (!('scrollTo' in HTMLElement.prototype)) {
  Object.defineProperty(HTMLElement.prototype, 'scrollTo', {
    value: vi.fn(),
    writable: true,
    configurable: true,
  })
}

/**
 * Mount the real Settings surface through the seam a user reaches it by —
 * the PaneContent, whose mount resolves only once the settings load has
 * landed. Waiting on that observable rather than on a duration is the
 * AGENTS.md rule and it is also the only thing that works: the load is a
 * promise chain the test does not hold.
 */
async function mountSettings(
  target: HTMLElement,
  store?: HistoryStatusStore,
): Promise<SettingsContent> {
  const client = new ProfileClient(new Dispatcher())
  vi.spyOn(client, 'describeSettings').mockResolvedValue({
    declarations: HISTORY_DECLS,
    groups: GROUPS,
    sectionGroups: { History: 'application', Terminal: 'application' },
  })
  vi.spyOn(client, 'getSnapshot').mockResolvedValue({ values: {}, overridden: [], revision: 0 })
  const content = new SettingsContent(
    client,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    undefined,
    store,
  )
  const host: PaneHost = {
    contentSettled: () => {},
    setTitle: vi.fn(),
    requestAttention: vi.fn(),
    requestClose: vi.fn(),
  }
  await content.mount(target, host, new AbortController().signal)
  await settle()
  // Settings opens on the first rail page — Connections, a component page —
  // so the History section is not in the DOM until the user navigates there.
  const historyLink = target.querySelector<HTMLButtonElement>(
    '.ui-grouped-nav__item[data-item="History"] > .ui-button',
  )
  expect(historyLink).toBeTruthy()
  historyLink!.click()
  await settle()
  return content
}

/** The section element the History declarations render into. */
function historySection(container: HTMLElement): HTMLElement {
  const el = container.querySelector<HTMLElement>('#st-section-History')
  if (el === null) throw new Error('History section not rendered')
  return el
}

/** Let Solid apply a signal written from outside a component. */
async function settle(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

describe('Settings → History says when durable history is not running', () => {
  let target: HTMLDivElement

  beforeEach(() => {
    document.body.replaceChildren()
    target = document.createElement('div')
    document.body.append(target)
  })

  it('renders the notice inside the History section, above the controls', async () => {
    const f = fakeClient(NO_KEY)
    const store = new HistoryStatusStore(f.client)
    store.start()
    await settle()

    await mountSettings(target, store)

    const section = historySection(target)
    const card = section.querySelector<HTMLElement>('.ui-status-card')
    expect(card).not.toBeNull()
    expect(card?.dataset.tone).toBe('warning')
    expect(card?.textContent).toContain('Commands are not being kept')
    expect(card?.textContent).toContain('key that encrypts')
    // The words the backend had for it reach the person who has to act.
    expect(card?.textContent).toContain('contentkey: open salt: is a directory')
    // The controls it contradicts are still there, and the notice is above
    // them: the first thing in the section, not a footnote under a toggle
    // that appears to work.
    expect(section.textContent).toContain('Keep command history')
    const first = section.querySelector<HTMLElement>('.ui-status-card, .ui-settings-row')
    expect(first?.classList.contains('ui-status-card')).toBe(true)
  })

  it('says nothing in the History section while history is running', async () => {
    const f = fakeClient(AVAILABLE)
    const store = new HistoryStatusStore(f.client)
    store.start()
    await settle()

    await mountSettings(target, store)
    expect(historySection(target).querySelector('.ui-status-card')).toBeNull()
  })

  it('says nothing anywhere else — this is the History section’s fact', async () => {
    const f = fakeClient(NO_KEY)
    const store = new HistoryStatusStore(f.client)
    store.start()
    await settle()

    await mountSettings(target, store)
    // Walk to the other section the way a person does. A section that is not
    // the open one is not in the page at all, so the only place this fact can
    // be checked is the section the person is actually looking at.
    const terminalLink = target.querySelector<HTMLButtonElement>(
      '.ui-grouped-nav__item[data-item="Terminal"] > .ui-button',
    )
    expect(terminalLink).toBeTruthy()
    terminalLink!.click()
    await settle()

    const terminal = target.querySelector<HTMLElement>('#st-section-Terminal')
    expect(terminal).not.toBeNull()
    expect(terminal?.querySelector('.ui-status-card')).toBeNull()
  })

  it('appears without a reload when the degrade is raised while Settings is open', async () => {
    const f = fakeClient(AVAILABLE)
    const store = new HistoryStatusStore(f.client)
    store.start()
    await settle()

    await mountSettings(target, store)
    expect(historySection(target).querySelector('.ui-status-card')).toBeNull()

    // The raise/clear push — the path nocx-rtg0.10's runtime failure takes.
    f.push(NO_KEY)
    await settle()
    expect(
      historySection(target).querySelector<HTMLElement>('.ui-status-card')?.textContent,
    ).toContain('Commands are not being kept')

    // And it goes away because something named made it go away, not because
    // it faded: that is the whole reason this is a status and not a toast.
    f.push(AVAILABLE)
    await settle()
    expect(historySection(target).querySelector('.ui-status-card')).toBeNull()
  })

  it('makes no claim at all when nothing supplies a status', async () => {
    // An embedding with no backend behind it (the dev-web harness). The
    // section renders its controls and says nothing either way — a
    // placeholder, not a lie in either direction.
    await mountSettings(target, undefined)
    expect(historySection(target).querySelector('.ui-status-card')).toBeNull()
    expect(historySection(target).textContent).toContain('Keep command history')
  })
})

// ── the discard: a working feature that starts from nothing (nocx-rtg0.19) ──

describe('historyDiscardSentence', () => {
  it('says nothing when nothing was discarded, which is every ordinary start', () => {
    // A notice on every start is a notice nobody reads by the third one.
    expect(historyDiscardSentence(aStatus())).toBeNull()
    expect(historyDiscardSentence(null)).toBeNull()
  })

  it('names the number, because an empty history looks like a fresh install', () => {
    const s = historyDiscardSentence(aStatus({ discarded: 42 }))
    expect(s).not.toBeNull()
    expect(s!.description).toContain('42 commands')
  })

  it('says one command in the singular', () => {
    expect(historyDiscardSentence(aStatus({ discarded: 1 }))!.description).toContain('1 command')
    expect(historyDiscardSentence(aStatus({ discarded: 1 }))!.description).not.toContain(
      '1 commands',
    )
  })

  it('still says it happened when the store could not count', () => {
    // -1 is "there was something and I could not count it". Silence would be
    // the one answer that is wrong: the rows are gone either way.
    const s = historyDiscardSentence(aStatus({ discarded: -1 }))
    expect(s).not.toBeNull()
    expect(s!.description).not.toContain('-1')
  })

  it('is a DIFFERENT fact from the unavailable notice, and both can be true', () => {
    // Folding them together would make the settings read as ungoverned when
    // they govern perfectly: history is running, it just starts empty.
    const running = aStatus({ discarded: 3 })
    expect(historyUnavailableSentence(running)).toBeNull()
    expect(historyDiscardSentence(running)).not.toBeNull()

    const down = aStatus({ available: false, reason: 'noKey', discarded: 3 })
    expect(historyUnavailableSentence(down)).not.toBeNull()
    expect(historyDiscardSentence(down)).not.toBeNull()
  })
})

// The runtime degrade (nocx-rtg0.10) — the only reason that ends without a
// restart, and the sentence says so.
describe('historyUnavailableSentence for a refusing store', () => {
  it('says commands run but are not kept, which is the whole distinction', () => {
    const s = historyUnavailableSentence(aStatus({ available: false, reason: 'writeFailed' }))
    expect(s).not.toBeNull()
    expect(s!.description).toContain('running')
    expect(s!.description).toContain('not being kept')
  })
})
