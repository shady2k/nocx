// @vitest-environment jsdom
// The Git view, through the REAL mountSidebar — the deliverable is an
// activity-bar view (rule 1: a user opens it from the rail and does the
// things below). The origin values are fixtures (the PaneContent capability
// is another worker's), while the whole mechanism around them — the signal,
// re-scope, staleness guards — is real.
//
// Named here, by the acceptance criteria: race 4 (a diff for a row clicked
// before the panel re-bound targets the click-time binding, with the frozen
// origin), the commit path (button exists, enabled from the state a user
// starts in, reaches the client), the row action owning its click, and the
// D14 absence (mutation controls ABSENT from an SSH tab's DOM, not disabled).
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { createSignal } from 'solid-js'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { mountSidebar, type SidebarHandle } from '../sidebar'
import { createGitView } from './git-view'
import { createGitStore, type GitStore } from './git-store'
import type { GitPanelServices } from './git-client'
import type { GitDiffTarget } from './git-diff/open-git-diff'
import type { Status } from '../generated/git.status'
import type { GitOpenResult } from '../generated/git.open'
import type { GitLogResult } from '../generated/git.log'
import type { ActiveOrigin } from '../pane-content'
import type { ClipboardAccess } from '../clipboard'
import type { UrlOpener } from '../open-url'
import { RpcError } from '../dispatcher'
import { ToastHost, clearToasts } from '../ui/toast'

// ── Fixtures ──────────────────────────────────────────────────────────────

const LOCAL_ORIGIN: ActiveOrigin = {
  paneId: 1,
  sessionId: 's1',
  kind: 'local',
  cwd: '/home/dev/repo',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

const OTHER_ORIGIN: ActiveOrigin = {
  paneId: 2,
  sessionId: 's2',
  kind: 'local',
  cwd: '/home/dev/other',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

const SSH_ORIGIN: ActiveOrigin = {
  paneId: 3,
  sessionId: 's3',
  kind: 'ssh',
  cwd: '/home/bob',
  cwdVerified: true,
  cwdFollow: true,
  host: 'srv',
}

const statusFixture = (over: Partial<Status> = {}): Status => ({
  branch: 'main',
  detached: false,
  unborn: false,
  head: 'abc1234',
  upstream: 'origin/main',
  ahead: 1,
  behind: 0,
  staged: [],
  unstaged: [],
  conflicted: [],
  total: 0,
  completeness: 'complete',
  ...over,
})

const openOk = (over: Partial<GitOpenResult & { state: 'ok' }> = {}): GitOpenResult => ({
  state: 'ok',
  bindingId: 'b1',
  toplevel: '/home/dev/repo',
  envState: 'resolved',
  status: statusFixture(),
  ...over,
})

const logFixture = (over: Partial<GitLogResult['log']> = {}): GitLogResult['log'] => ({
  entries: [
    {
      hash: '5738d62b66777a78af894c0708d3a7e8798a4d8d',
      shortHash: '5738d62',
      subject: 'third',
      authorName: 'Test Author',
      authoredAt: '2020-01-01T00:00:00Z',
      refs: ['main'],
    },
    {
      hash: '98c56f29de7a461cbbb7bc3a208a292972265b76',
      shortHash: '98c56f2',
      subject: 'second subject',
      authorName: 'Test Author',
      authoredAt: '2020-01-02T00:00:00Z',
      refs: ['HEAD', 'v1.0'],
    },
  ],
  total: 2,
  completeness: 'complete',
  ...over,
})
function fakeServices(over: Partial<GitPanelServices> = {}): GitPanelServices {
  return {
    open: vi.fn().mockResolvedValue(openOk()),
    status: vi.fn().mockResolvedValue({ status: statusFixture(), envState: 'resolved' }),
    diff: vi.fn().mockResolvedValue({ state: 'ok', text: '', truncated: false }),
    log: vi.fn().mockResolvedValue({ log: logFixture() }),
    stage: vi.fn().mockResolvedValue({ status: statusFixture() }),
    unstage: vi.fn().mockResolvedValue({ status: statusFixture() }),
    stageAll: vi.fn().mockResolvedValue({ status: statusFixture() }),
    unstageAll: vi.fn().mockResolvedValue({ status: statusFixture() }),
    commit: vi.fn().mockResolvedValue({ state: 'ok', outputTruncated: false }),
    headMessage: vi.fn().mockResolvedValue({ state: 'ok', message: 'head' }),
    // none by default: no recognised remote means no open-link affordance
    // (D14), which is the DOM every pre-existing test expects. Tests that
    // exercise the links override with an ok remote.
    remote: vi.fn().mockResolvedValue({ state: 'none' }),
    openUrl: vi.fn().mockResolvedValue({}),
    close: vi.fn().mockResolvedValue({ closed: true }),
    subscribeGitChanged: vi.fn().mockReturnValue(() => {}),
    ...over,
  }
}

async function settle(): Promise<void> {
  for (let i = 0; i < 8; i++) await Promise.resolve()
}

interface Mounted {
  panel: HTMLElement
  open: ReturnType<typeof vi.fn>
  setActiveOrigin: (o: ActiveOrigin | null) => void
  services: GitPanelServices
  store: GitStore
  handle: SidebarHandle
}

const stores: GitStore[] = []

/** The pieces a second mount must share with the first — the shape the real
 *  app has: ONE store and ONE origin accessor, with the panel mounting and
 *  unmounting on top of them (design §5.5). */
interface SharedMount {
  store: GitStore
  origin: [() => ActiveOrigin | null, (o: ActiveOrigin | null) => void]
}

function mountApp(
  services: GitPanelServices,
  shared?: SharedMount,
  clipboard?: ClipboardAccess,
  urlOpener?: UrlOpener,
): Mounted {
  const open = vi.fn()
  const store = shared?.store ?? createGitStore(services)
  if (!stores.includes(store)) stores.push(store)
  // The rule cannot see through a conditional source; the createSignal call
  // IS array-destructured, and the shared branch re-uses the first mount's
  // pair — the app's one-origin, one-store shape (design §5.5).
  const [activeOrigin, setActiveOrigin] =
    // eslint-disable-next-line solid/reactivity -- conditional destructure source
    shared === undefined ? createSignal<ActiveOrigin | null>(null) : shared.origin
  const git = createGitView({
    services,
    store,
    opener: { open },
    activeOrigin,
    clipboard,
    urlOpener,
  })
  const bar = document.createElement('div')
  bar.id = 'activitybar'
  const panel = document.createElement('div')
  panel.id = 'sidebar'
  document.body.append(bar, panel)
  const handle = mountSidebar(
    bar,
    panel,
    [git],
    [],
    undefined,
    () => null,
    () => activeOrigin(),
  )
  // A ToastHost so action outcomes (copied branch, refused browser open) are
  // ASSERTABLE as rendered toasts, the way a user sees them — the files
  // pattern.
  render(() => <ToastHost />)
  return { panel, open, setActiveOrigin, services, store, handle }
}

afterEach(() => {
  clearToasts()
  for (const s of stores) s.dispose()
  stores.length = 0
  cleanup()
})

/** The disclosure button of the section whose title contains `title` — the
 *  control a user clicks to fold a section's rows away (nocx-nak2). */
function sectionDisclosure(panel: HTMLElement, title: string): HTMLButtonElement {
  const sections = panel.querySelectorAll<HTMLElement>('.ui-section')
  for (const section of sections) {
    if (section.textContent?.includes(title)) {
      const button = section.querySelector<HTMLButtonElement>('.ui-section__disclosure')
      if (button !== null) return button
    }
  }
  throw new Error(`no disclosure for section ${title}`)
}

/** The listitem whose text contains `path` — the row a user clicks. */
function rowNamed(panel: HTMLElement, path: string): HTMLElement {
  const rows = panel.querySelectorAll<HTMLElement>('[role="listitem"]')
  for (const row of rows) {
    if (row.textContent?.includes(path)) return row
  }
  throw new Error(`no row for ${path}`)
}

const unstagedFile = statusFixture({
  unstaged: [{ path: 'a.txt', x: '.', y: 'M' }],
  total: 1,
})

const stagedFile = statusFixture({
  staged: [{ path: 'a.txt', x: 'A', y: '.' }],
  total: 1,
})

// ── The states a user can land in ────────────────────────────────────────

describe('the panel renders what the store says', () => {
  it('noPane: no origin — the empty state', () => {
    const { panel } = mountApp(fakeServices())
    expect(panel.textContent).toContain('No repository to show')
  })

  it('remote: the mutation controls are ABSENT from the DOM, not disabled (D14)', async () => {
    const { panel, setActiveOrigin } = mountApp(fakeServices())
    setActiveOrigin(SSH_ORIGIN)
    await settle()
    expect(panel.textContent).toContain("Git on a remote host isn't supported yet")
    expect(panel.querySelector('[data-testid="git-stage-all"]')).toBeNull()
    expect(panel.querySelector('[data-testid="git-unstage-all"]')).toBeNull()
    expect(panel.querySelector('[data-testid="git-commit"]')).toBeNull()
  })

  it('ready: the lists and the header render from the wire status', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: unstagedFile })),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    expect(panel.querySelector('[data-testid="git-branch"]')?.textContent).toContain('main')
    expect(panel.querySelector('[data-testid="git-unstaged-list"]')?.textContent).toContain('a.txt')
  })

  it('tooManyChanges: the D9 cap banner says which answer it is, over the retained lists', async () => {
    const capped = statusFixture({
      completeness: 'capped',
      total: 6000,
      unstaged: [{ path: 'a.txt', x: '.', y: 'M' }],
    })
    const services = fakeServices({ open: vi.fn().mockResolvedValue(openOk({ status: capped })) })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    const banner = panel.querySelector('[data-testid="git-too-many-changes"]')
    expect(banner?.textContent).toContain('6000 changes, showing the first 1')
    // The retained row still renders under the banner.
    expect(panel.querySelector('[data-testid="git-unstaged-list"]')?.textContent).toContain('a.txt')
  })

  it('conflicted: stage-all and unstage-all are refused, visibly and with the reason (D19)', async () => {
    const conflicted = statusFixture({
      conflicted: [{ path: 'conf.txt', x: 'U', y: 'U' }],
      total: 1,
    })
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: conflicted })),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    expect(panel.querySelector('[data-testid="git-stage-all"]')?.hasAttribute('disabled')).toBe(
      true,
    )
    expect(panel.querySelector('[data-testid="git-unstage-all"]')?.hasAttribute('disabled')).toBe(
      true,
    )
    expect(panel.querySelector('[data-testid="git-conflict-refusal"]')?.textContent).toContain(
      'Unresolved merge conflicts',
    )
    // The conflicted file shows with its status letter and no actions.
    expect(panel.querySelector('[data-testid="git-conflicted-list"]')?.textContent).toContain(
      'conf.txt',
    )
  })

  it('a conflict that DEVELOPS while the panel is open refuses too, and clearing it releases', async () => {
    // The defect an e2e caught and this suite did not. Every conflict test
    // above opens ONTO an already-conflicted repository, so a predicate that
    // read the status untracked agreed with all of them while never
    // re-evaluating. A merge happens in the terminal beside the panel, which
    // makes "the conflict arrives later" the ordinary case — and it left the
    // two destructive controls enabled with no reason shown.
    //
    // It has to be asserted HERE and not in the store: called directly, the
    // predicate returns the right answer either way. What broke was the
    // SUBSCRIPTION, and only a render can see that.
    const clean = statusFixture({ total: 0 })
    const conflicted = statusFixture({
      conflicted: [{ path: 'conf.txt', x: 'U', y: 'U' }],
      total: 1,
    })
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: clean })),
      status: vi
        .fn()
        .mockResolvedValueOnce({ status: conflicted, envState: 'resolved' })
        .mockResolvedValue({ status: clean, envState: 'resolved' }),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    expect(panel.querySelector('[data-testid="git-stage-all"]')?.hasAttribute('disabled')).toBe(
      false,
    )
    expect(panel.querySelector('[data-testid="git-conflict-refusal"]')).toBeNull()

    panel.querySelector<HTMLElement>('[data-testid="git-refresh"]')?.click()
    await settle()
    expect(panel.querySelector('[data-testid="git-stage-all"]')?.hasAttribute('disabled')).toBe(
      true,
    )
    expect(panel.querySelector('[data-testid="git-unstage-all"]')?.hasAttribute('disabled')).toBe(
      true,
    )
    expect(panel.querySelector('[data-testid="git-conflict-refusal"]')?.textContent).toContain(
      'Unresolved merge conflicts',
    )

    panel.querySelector<HTMLElement>('[data-testid="git-refresh"]')?.click()
    await settle()
    expect(panel.querySelector('[data-testid="git-conflict-refusal"]')).toBeNull()
    expect(panel.querySelector('[data-testid="git-stage-all"]')?.hasAttribute('disabled')).toBe(
      false,
    )
  })

  it('the degraded warning appears at open and is withdrawn by the poll that carries resolved (nocx-69ey)', async () => {
    const reason =
      'the shell environment has not been resolved yet; the first commit will wait for it'
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ envState: 'degraded', envReason: reason })),
      status: vi
        .fn()
        .mockResolvedValueOnce({ status: statusFixture(), envState: 'degraded', envReason: reason })
        .mockResolvedValue({ status: statusFixture(), envState: 'resolved' }),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    // The open landed in the pre-settle window: the warning is on screen.
    const warning = panel.querySelector('[data-testid="git-env-degraded"]')
    expect(warning?.textContent).toContain('degraded environment')

    // A poll that still sees the in-flight resolution keeps it there.
    panel.querySelector<HTMLElement>('[data-testid="git-refresh"]')?.click()
    await settle()
    expect(panel.querySelector('[data-testid="git-env-degraded"]')).not.toBeNull()

    // The background resolution settles; a poll carries resolved and the
    // warning is withdrawn — the same binding, no re-open.
    panel.querySelector<HTMLElement>('[data-testid="git-refresh"]')?.click()
    await settle()
    expect(panel.querySelector('[data-testid="git-env-degraded"]')).toBeNull()
  })
})

// ── Race 4: a diff for a row clicked before the panel re-bound ───────────

describe('race 4 — the diff target captures the click-time binding', () => {
  it("a row clicked before the panel re-bound opens the diff under the ROW's binding, with the frozen origin", async () => {
    const services = fakeServices({
      open: vi
        .fn()
        .mockResolvedValueOnce(openOk({ status: unstagedFile })) // tab A
        .mockResolvedValueOnce(openOk({ bindingId: 'b2', toplevel: '/home/dev/other' })), // tab B
    })
    const { panel, open, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    // The user clicks the row — the handler captures the binding as it is
    // at the click: b1, before any re-bind.
    fireEvent.click(rowNamed(panel, 'a.txt'))
    expect(open).toHaveBeenCalledTimes(1)
    const target = open.mock.calls[0][0] as GitDiffTarget
    expect(target.bindingId).toBe('b1')
    expect(target.toplevel).toBe('/home/dev/repo')
    expect(target.path).toBe('a.txt')
    expect(target.side).toBe('unstaged')
    // The frozen origin: the diff tab answers activeOrigin() as the same
    // machine, with NO opinion about where the shell is now — the panel
    // keeps the binding the tab reads through.
    expect(target.origin).toEqual({
      sessionId: 's1',
      kind: 'local',
      cwd: '/home/dev/repo',
      cwdVerified: true,
      host: null,
      cwdFollow: false,
    })

    // The panel then re-binds to tab B. The diff already issued under b1 —
    // the click-time binding, never the re-bound one.
    setActiveOrigin(OTHER_ORIGIN)
    await settle()
    expect(open).toHaveBeenCalledTimes(1)
  })

  it('an untracked row opens the untracked side (diff against /dev/null)', async () => {
    const untracked = statusFixture({
      unstaged: [{ path: 'new.txt', x: '?', y: '?' }],
      total: 1,
    })
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: untracked })),
    })
    const { panel, open, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    fireEvent.click(rowNamed(panel, 'new.txt'))
    const target = open.mock.calls[0][0] as GitDiffTarget
    expect(target.side).toBe('untracked')
  })
})

// ── The commit path — what a user can do (rule 1) ────────────────────────

describe('the commit path', () => {
  it('the Commit button exists, is disabled from the state a user starts in, and is enabled after staging a file and typing a subject — and reaches the client', async () => {
    const stage = vi.fn().mockResolvedValue({ status: stagedFile })
    const commit = vi.fn().mockResolvedValue({ state: 'ok', outputTruncated: false })
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: unstagedFile })),
      stage,
      commit,
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    const commitButton = () => panel.querySelector<HTMLButtonElement>('[data-testid="git-commit"]')
    // The state a user starts in: nothing staged, empty subject — disabled.
    expect(commitButton()?.disabled).toBe(true)

    // Stage the file from the row's own control.
    fireEvent.click(panel.querySelector('[data-testid="git-row-stage"]') as HTMLElement)
    await settle()
    expect(stage).toHaveBeenCalledWith('b1', ['a.txt'])

    // Still disabled: a staged file with an empty subject is not a commit.
    expect(commitButton()?.disabled).toBe(true)

    // Type a subject.
    const subject = panel.querySelector('#git-commit-subject') as HTMLInputElement
    fireEvent.input(subject, { target: { value: 'my subject' } })
    await settle()
    expect(commitButton()?.disabled).toBe(false)

    // Commit reaches the client.
    fireEvent.click(commitButton() as HTMLButtonElement)
    await settle()
    expect(commit).toHaveBeenCalledWith('b1', 'my subject', false)
  })

  it('a failed commit shows git output with the truncation mark and keeps the typed message (D11)', async () => {
    const commit = vi.fn().mockResolvedValue({
      state: 'failed',
      output: 'error: pre-commit hook failed\n  lint\n',
      outputTruncated: true,
    })
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: stagedFile })),
      commit,
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    const subject = panel.querySelector('#git-commit-subject') as HTMLInputElement
    fireEvent.input(subject, { target: { value: 'keep me' } })
    fireEvent.click(panel.querySelector('[data-testid="git-commit"]') as HTMLButtonElement)
    await settle()
    expect(panel.querySelector('[data-testid="git-commit-output"]')?.textContent).toContain(
      'pre-commit hook failed',
    )
    expect(panel.querySelector('[data-testid="git-commit-output-truncated"]')).not.toBeNull()
    // The message stays in the form.
    expect((panel.querySelector('#git-commit-subject') as HTMLInputElement).value).toBe('keep me')
  })

  it('a nothing-to-commit refusal is a toast, not a silent re-open (nocx-bpqil)', async () => {
    // ErrNothingToCommit shares -32602 with unknown-binding; before the
    // reason discriminator the store re-resolved it into a silent no-op.
    // Now it reaches the mutationFailureMessage branch a person reads.
    const commit = vi
      .fn()
      .mockRejectedValue(
        new RpcError('git: nothing is staged to commit', -32602, { reason: 'nothing-to-commit' }),
      )
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: stagedFile })),
      commit,
    })
    const { panel, open, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    fireEvent.input(panel.querySelector('#git-commit-subject') as HTMLInputElement, {
      target: { value: 'doomed' },
    })
    fireEvent.click(panel.querySelector('[data-testid="git-commit"]') as HTMLButtonElement)
    await settle()
    expect(open).not.toHaveBeenCalled()
    expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
      'Nothing is staged to commit — stage a file first.',
    )
  })

  it('an amend-on-unborn refusal is a toast, not a silent re-open (nocx-bpqil)', async () => {
    const commit = vi.fn().mockRejectedValue(
      new RpcError('git: cannot amend a commit on an unborn branch', -32602, {
        reason: 'amend-unborn',
      }),
    )
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: stagedFile })),
      commit,
    })
    const { panel, open, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    // Tick Amend so the commit is an amend — the user's seam is the kit
    // checkbox beside the form (label "Amend last commit").
    const amendBox = panel.querySelector<HTMLInputElement>(
      '.git-commit-form input[type="checkbox"]',
    )
    expect(amendBox).not.toBeNull()
    fireEvent.click(amendBox as HTMLInputElement)
    await settle()
    fireEvent.input(panel.querySelector('#git-commit-subject') as HTMLInputElement, {
      target: { value: 'doomed' },
    })
    fireEvent.click(panel.querySelector('[data-testid="git-commit"]') as HTMLButtonElement)
    await settle()
    expect(open).not.toHaveBeenCalled()
    expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
      'This branch has no commit to amend yet.',
    )
  })
})

// ── The row action owns its click ────────────────────────────────────────

describe('row actions', () => {
  it('the stage control reaches the store and never opens the diff — the kit guarantee, proven anyway', async () => {
    const stage = vi.fn().mockResolvedValue({ status: stagedFile })
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: unstagedFile })),
      stage,
    })
    const { panel, open, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    fireEvent.click(panel.querySelector('[data-testid="git-row-stage"]') as HTMLElement)
    await settle()
    expect(stage).toHaveBeenCalledWith('b1', ['a.txt'])
    expect(open).not.toHaveBeenCalled()
    // The row moved to Staged.
    expect(panel.querySelector('[data-testid="git-staged-list"]')?.textContent).toContain('a.txt')
  })

  it("a typechange (T) row renders as the kit's modification letter", async () => {
    const typed = statusFixture({
      unstaged: [{ path: 'bin', x: '.', y: 'T' }],
      total: 1,
    })
    const services = fakeServices({ open: vi.fn().mockResolvedValue(openOk({ status: typed })) })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    // The row's status glyph is the kit's M — never an empty tone.
    const row = rowNamed(panel, 'bin')
    expect(row.querySelector('.ui-file-status-row__status')?.textContent).toBe('M')
  })

  it('the diff opens for a staged row with the staged side', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: stagedFile })),
    })
    const { panel, open, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    fireEvent.click(rowNamed(panel, 'a.txt'))
    const target = open.mock.calls[0][0] as GitDiffTarget
    expect(target.side).toBe('staged')
  })

  it('a rejected stage is a danger toast with the mapped sentence — never a div in the panel', async () => {
    // A dropped socket rejects with the transport's plain words (no RPC
    // code), which is the shape the connection classifier maps. Domain
    // refusals carry data.reason (nocx-bpqil): only reason
    // "unknown-binding" re-resolves; every other refusal reaches this
    // toast as the refusal it is.
    const stage = vi.fn().mockRejectedValue(new Error('ws closed'))
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: unstagedFile })),
      stage,
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    fireEvent.click(panel.querySelector('[data-testid="git-row-stage"]') as HTMLElement)
    await settle()
    // The failure is an action outcome, announced by the kit toast the way
    // a user sees it. The raw wire words live in the toast, never in the
    // document flow: the panel body must not hold the message (the old
    // git-mutation-error div rendered exactly that).
    expect(panel.textContent).not.toContain('ws closed')
    expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
      'The change could not be made — the connection was lost.',
    )
  })

  it('a conflicted refusal reaches the toast as itself — never a silent re-open (nocx-bpqil)', async () => {
    // The old isUnknownBinding swallowed every -32602, so a conflicted
    // stage-all re-resolved through git.open and the refusal was lost.
    // Now the store re-resolves only on reason "unknown-binding": a
    // conflicted entry (same code, reason "conflicted") reaches the
    // mutationFailureMessage branch a person should read.
    const stage = vi.fn().mockRejectedValue(
      new RpcError('git: cannot stage or unstage all while "conf.txt" is conflicted', -32602, {
        reason: 'conflicted',
      }),
    )
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: unstagedFile })),
      stage,
    })
    const { panel, open, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    fireEvent.click(panel.querySelector('[data-testid="git-row-stage"]') as HTMLElement)
    await settle()
    // NOT re-opened: the binding was fine; the repository refused.
    expect(open).not.toHaveBeenCalled()
    // The mapped sentence is the refusal a person reads.
    expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
      'The change could not be made while a merge conflict is unresolved.',
    )
  })

  it('a not-owned refusal is a toast — the repository is gone from this view, not silently re-opened (nocx-bpqil)', async () => {
    // ErrNotOwned shares -32602 with unknown-binding. The binding EXISTS —
    // it belongs to another session — so re-opening through git.open could
    // not fix it and would mint a second binding for a repository this view
    // cannot own. The reason discriminator keeps it a visible refusal.
    const stage = vi
      .fn()
      .mockRejectedValue(
        new RpcError(
          'git: binding "b1" belongs to session "other", which the caller does not own',
          -32602,
          { reason: 'not-owned' },
        ),
      )
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: unstagedFile })),
      stage,
    })
    const { panel, open, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    fireEvent.click(panel.querySelector('[data-testid="git-row-stage"]') as HTMLElement)
    await settle()
    expect(open).not.toHaveBeenCalled()
    expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
      "The change could not be made — this view's repository is no longer available.",
    )
  })

  it('a rejected commit appends the raw reason — the open sentence never swallows it', async () => {
    const commit = vi.fn().mockRejectedValue(new RpcError('index.lock exists', -32603))
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: stagedFile })),
      commit,
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    fireEvent.input(panel.querySelector('#git-commit-subject') as HTMLInputElement, {
      target: { value: 'doomed' },
    })
    fireEvent.click(panel.querySelector('[data-testid="git-commit"]') as HTMLButtonElement)
    await settle()
    expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
      'The change could not be made (index.lock exists).',
    )
  })

  it("a saturation refusal is the dispatcher's own toast — the panel does not toast twice", async () => {
    const stage = vi.fn().mockRejectedValue(new RpcError('Control plane busy', -32004))
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: unstagedFile })),
      stage,
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    fireEvent.click(panel.querySelector('[data-testid="git-row-stage"]') as HTMLElement)
    await settle()
    expect(document.querySelector('.ui-toast__message')).toBeNull()
    // The store still holds the account, so the panel does not silently
    // swallow the refusal — the stale banner carries the recovery.
    expect(panel.querySelector('[data-testid="git-status-stale"]')).not.toBeNull()
  })
})

// ── The Commits section (brief, git.log) ──────────────────────────────────

describe('the Commits section', () => {
  it('lists the branch commits newest first, with subject, hash, relative time and refs', async () => {
    const { panel, setActiveOrigin } = mountApp(fakeServices())
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    const log = panel.querySelector('[data-testid="git-log"]')
    expect(log).not.toBeNull()
    const rows = panel.querySelectorAll('[data-testid="git-log-row"]')
    // Newest first: the fixture's stream order is the render order.
    expect(rows[0]?.textContent).toContain('third')
    expect(rows[0]?.textContent).toContain('5738d62')
    expect(rows[1]?.textContent).toContain('second subject')
    // The refs are the kit's chips; a bare HEAD is the detached marker.
    const refs = panel.querySelectorAll('[data-testid="git-log-ref"]')
    expect(refs[0]?.textContent).toContain('main')
    expect(refs[1]?.textContent).toContain('HEAD')
    expect(refs[2]?.textContent).toContain('v1.0')
  })

  it('an unborn branch renders "No commits yet" — the empty list is a state, not a failure', async () => {
    const services = fakeServices({
      log: vi.fn().mockResolvedValue({ log: logFixture({ entries: [], total: 0 }) }),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    expect(panel.querySelector('[data-testid="git-log-empty"]')?.textContent).toContain(
      'No commits yet',
    )
  })

  it('a capped log says so — the bounded read must not look complete (D9)', async () => {
    const services = fakeServices({
      log: vi.fn().mockResolvedValue({
        log: logFixture({ entries: logFixture().entries, total: 51, completeness: 'capped' }),
      }),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    expect(panel.querySelector('[data-testid="git-log-capped"]')?.textContent).toContain(
      'More than 2 commits',
    )
  })

  it('a failed read renders the failure with Retry, and the rest of the panel stays live', async () => {
    const services = fakeServices({
      log: vi.fn().mockRejectedValue(new Error('git log: exit 128: fatal: bad object HEAD')),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    expect(panel.querySelector('[data-testid="git-log-failed"]')?.textContent).toContain(
      'bad object HEAD',
    )
    expect(panel.querySelector('[data-testid="git-log-retry"]')).not.toBeNull()
    // The status half is untouched by a failed commits read.
    expect(panel.querySelector('[data-testid="git-branch"]')?.textContent).toContain('main')
  })
})

// ── The collapsible sections (nocx-nak2) ──────────────────────────────────

describe('the collapsible sections', () => {
  it('clicking a disclosure folds the rows away and keeps the heading and count; clicking again restores them', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: unstagedFile })),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    const disclosure = sectionDisclosure(panel, 'Unstaged')
    expect(disclosure.getAttribute('aria-expanded')).toBe('true')
    expect(panel.querySelector('[data-testid="git-unstaged-list"]')).not.toBeNull()

    fireEvent.click(disclosure)
    await settle()
    // The rows are gone; the heading and its count remain.
    expect(panel.querySelector('[data-testid="git-unstaged-list"]')).toBeNull()
    expect(panel.textContent).toContain('Unstaged (1)')
    expect(sectionDisclosure(panel, 'Unstaged').getAttribute('aria-expanded')).toBe('false')

    fireEvent.click(sectionDisclosure(panel, 'Unstaged'))
    await settle()
    expect(panel.querySelector('[data-testid="git-unstaged-list"]')).not.toBeNull()
    expect(sectionDisclosure(panel, 'Unstaged').getAttribute('aria-expanded')).toBe('true')
  })

  it('a collapse is driven by the store — the panel passes the state and reports toggles through it', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: unstagedFile })),
    })
    const { panel, setActiveOrigin, store } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    expect(store.sectionOpen('unstaged')).toBe(true)
    fireEvent.click(sectionDisclosure(panel, 'Unstaged'))
    await settle()
    expect(store.sectionOpen('unstaged')).toBe(false)
    // The disclosure renders what the store says: aria-expanded tracks it.
    expect(sectionDisclosure(panel, 'Unstaged').getAttribute('aria-expanded')).toBe('false')
  })

  it('a collapse survives the panel unmounting and remounting — the state lives in the store, not the component', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: unstagedFile })),
      // The visibility effect polls the moment the panel is seen again; the
      // fixture repository has not changed, so the poll must answer the
      // same status (the fake's default answers an empty one).
      status: vi.fn().mockResolvedValue({ status: unstagedFile }),
    })
    const store = createGitStore(services)
    const [activeOrigin, setActiveOrigin] = createSignal<ActiveOrigin | null>(null)

    const first = mountApp(services, { store, origin: [activeOrigin, setActiveOrigin] })
    first.setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    fireEvent.click(sectionDisclosure(first.panel, 'Unstaged'))
    await settle()
    expect(first.panel.querySelector('[data-testid="git-unstaged-list"]')).toBeNull()

    // The view switch: the panel unmounts; the store lives on (design §5.5).
    first.handle.destroy()

    // Back to the view: a fresh mount over the SAME store and origin.
    const second = mountApp(services, { store, origin: [activeOrigin, setActiveOrigin] })
    await settle()
    expect(sectionDisclosure(second.panel, 'Unstaged').getAttribute('aria-expanded')).toBe('false')
    expect(second.panel.querySelector('[data-testid="git-unstaged-list"]')).toBeNull()
    expect(second.panel.textContent).toContain('Unstaged (1)')
  })

  it('a collapse does not leak across a repository re-bind — it belongs to one repository', async () => {
    const services = fakeServices({
      open: vi
        .fn()
        .mockResolvedValueOnce(openOk({ status: unstagedFile })) // repo A
        .mockResolvedValueOnce(openOk({ bindingId: 'b2', toplevel: '/home/dev/other' })), // repo B
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    fireEvent.click(sectionDisclosure(panel, 'Unstaged'))
    await settle()
    expect(sectionDisclosure(panel, 'Unstaged').getAttribute('aria-expanded')).toBe('false')

    // The shell moves to another repository: the panel re-binds, and the
    // collapse must not follow it there.
    setActiveOrigin(OTHER_ORIGIN)
    await settle()
    expect(sectionDisclosure(panel, 'Unstaged').getAttribute('aria-expanded')).toBe('true')
  })
})

// ── Copy the branch, open on hosting (brief, nocx-hc0m) ─────────────────

/** A clipboard recorder for the success path: writes are observable. */
function recorderClipboard(): ClipboardAccess & { writes: string[] } {
  const writes: string[] = []
  return {
    writes,
    readText: vi.fn().mockResolvedValue(''),
    writeText: vi.fn().mockImplementation((t: string) => {
      writes.push(t)
      return Promise.resolve()
    }),
  }
}
/** A clipboard that refuses every write — the platform-rejection path. */
const refusingClipboard: ClipboardAccess = {
  readText: () => Promise.reject(new Error('refused')),
  writeText: () => Promise.reject(new Error('refused')),
}

/** A recognised GitHub remote, for the open-on-hosting cases. */
const githubRemote = (over: Partial<GitPanelServices> = {}) =>
  fakeServices({
    remote: vi.fn().mockResolvedValue({ state: 'ok', url: 'git@github.com:shady2k/nocx.git' }),
    ...over,
  })

describe('copy the branch and open on hosting (brief, nocx-hc0m)', () => {
  it('copy: one click writes the branch name through the seam and the panel confirms', async () => {
    const services = githubRemote()
    const clip = recorderClipboard()
    const { panel, setActiveOrigin } = mountApp(services, undefined, clip)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    const copy = panel.querySelector<HTMLElement>('[data-testid="git-copy-branch"]')
    expect(copy).not.toBeNull()
    copy!.click()
    await settle()

    // The one action copied the REAL branch name — never the "no commits
    // yet" label — and the confirmation is the panel's own toast.
    expect(clip.writes).toEqual(['main'])
    expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
      'Branch name copied',
    )
  })

  it('a refused clipboard write is told, never swallowed', async () => {
    const services = fakeServices()
    const { panel, setActiveOrigin } = mountApp(services, undefined, refusingClipboard)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    panel.querySelector<HTMLElement>('[data-testid="git-copy-branch"]')!.click()
    await settle()
    expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
      "Couldn't copy the branch name",
    )
  })

  it('a detached HEAD has no branch name, so the copy affordance is absent (D14)', async () => {
    const services = fakeServices({
      open: vi
        .fn()
        .mockResolvedValue(
          openOk({ status: statusFixture({ detached: true, branch: '', head: 'abc1234' }) }),
        ),
    })
    const { panel, setActiveOrigin } = mountApp(services, undefined, recorderClipboard())
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    expect(panel.querySelector('[data-testid="git-copy-branch"]')).toBeNull()
  })

  /** A recorder for the URL-open seam: calls are observable, and a
   *  rejection is the failure path — exactly like the real capability
   *  (open-url.ts), which the view defaults to. */
  interface UrlOpenerRecorder extends UrlOpener {
    open: Mock<(url: string) => Promise<void>>
  }
  function recorderUrlOpener(): UrlOpenerRecorder {
    return { open: vi.fn<(url: string) => Promise<void>>().mockResolvedValue(undefined) }
  }
  it('open branch: the link is drawn for a recognised remote and hands the derived URL to the shared capability', async () => {
    const urlOpener = recorderUrlOpener()
    const services = githubRemote()
    const { panel, setActiveOrigin } = mountApp(services, undefined, recorderClipboard(), urlOpener)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    const open = panel.querySelector<HTMLElement>('[data-testid="git-open-branch"]')
    expect(open).not.toBeNull()
    open!.click()
    await settle()

    // The URL the panel derived — git's own remote spelling, converted —
    // is exactly what reaches the shared URL opener.
    expect(urlOpener.open).toHaveBeenCalledWith('https://github.com/shady2k/nocx/tree/main')
  })

  it('open commit: a commit row carries the link and it clicks through the same seam', async () => {
    const urlOpener = recorderUrlOpener()
    const services = githubRemote()
    const { panel, setActiveOrigin } = mountApp(services, undefined, recorderClipboard(), urlOpener)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    const open = panel.querySelector<HTMLElement>('[data-testid="git-open-commit"]')
    expect(open).not.toBeNull()
    open!.click()
    await settle()

    expect(urlOpener.open).toHaveBeenCalledWith(
      'https://github.com/shady2k/nocx/commit/5738d62b66777a78af894c0708d3a7e8798a4d8d',
    )
  })

  it('a refused open toasts the refusal, never a silent no-op', async () => {
    const urlOpener = recorderUrlOpener()
    urlOpener.open.mockRejectedValue(new Error('unavailable'))
    const services = githubRemote()
    const { panel, setActiveOrigin } = mountApp(services, undefined, recorderClipboard(), urlOpener)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    panel.querySelector<HTMLElement>('[data-testid="git-open-branch"]')!.click()
    await settle()
    expect(document.querySelector('.ui-toast__message')?.textContent).toContain(
      "Couldn't open the link in your browser",
    )
  })

  it('the web open happens synchronously in the click gesture — no await between click and window.open', async () => {
    // The whole point of the capability's shape (open-url.ts): the panel
    // must reach window.open in the same tick as the click, or popup
    // blockers eat the gesture. This drives the REAL seam — the view's
    // default opener over the services — with window.open recorded, and
    // asserts the call lands before any promise microtask could have run.
    // A future "tidy" that awaits before opening flips this test red.
    const win = vi.spyOn(window, 'open').mockReturnValue({} as Window)
    const services = githubRemote()
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    let microtaskRan = false
    void Promise.resolve().then(() => {
      microtaskRan = true
    })

    panel.querySelector<HTMLElement>('[data-testid="git-open-branch"]')!.click()

    expect(win).toHaveBeenCalledWith(
      'https://github.com/shady2k/nocx/tree/main',
      '_blank',
      'noopener,noreferrer',
    )
    expect(microtaskRan).toBe(false)
    win.mockRestore()
  })

  it('D14: with no recognised remote the open links are absent, never disabled', async () => {
    const services = fakeServices() // remote: none by default
    const { panel, setActiveOrigin } = mountApp(services, undefined, recorderClipboard())
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    expect(panel.querySelector('[data-testid="git-open-branch"]')).toBeNull()
    expect(panel.querySelector('[data-testid="git-open-commit"]')).toBeNull()
    // The copy affordance is unrelated to the remote: a branch name exists.
    expect(panel.querySelector('[data-testid="git-copy-branch"]')).not.toBeNull()
  })
})

// ── The path filter (nocx-52by) ─────────────────────────────────────────

/** The search input of the panel's filter — the control a user types into. */
function filterInput(panel: HTMLElement): HTMLInputElement {
  const input = panel.querySelector<HTMLInputElement>('.ui-search-field__input')
  if (input === null) throw new Error('no filter input')
  return input
}

function typeFilter(panel: HTMLElement, value: string): void {
  fireEvent.input(filterInput(panel), { target: { value } })
}

const severalFiles = statusFixture({
  staged: [
    { path: 'staged/a.txt', x: 'A', y: '.', added: 1, deleted: 0 },
    { path: 'staged/b.txt', x: 'A', y: '.', added: 1, deleted: 0 },
  ],
  unstaged: [
    { path: 'src/git-panel.tsx', x: '.', y: 'M', added: 5, deleted: 2 },
    { path: 'docs/guide.md', x: '.', y: 'M', added: 3, deleted: 1 },
    { path: 'notes.txt', x: '.', y: 'M', added: 1, deleted: 0 },
  ],
  total: 5,
})

describe('the path filter (nocx-52by)', () => {
  it('is pinned above the scroller, where it cannot scroll away (nocx-708q.3)', async () => {
    // It used to stand above the two lists INSIDE the body, so scrolling a
    // repository with more changes than fit took the control that narrows
    // them off the top of the panel (owner, 2026-08-22). It is the
    // descriptor's `filter` slot now, and the shell pins it.
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: severalFiles })),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    const field = filterInput(panel)
    expect(field.closest('.ui-sidebar-view__filter')).not.toBeNull()
    expect(field.closest('.ui-sidebar-view__body')).toBeNull()
    // And the rows it narrows are inside the scroller, which is the whole
    // point of the pair.
    expect(
      panel.querySelector('[data-testid="git-unstaged-list"]')?.closest('.ui-sidebar-view__body'),
    ).not.toBeNull()
  })

  it('offers no field in a state that holds no list', async () => {
    // Not a repository: the panel is one StatusCard saying so. A search box
    // above an explanation of why there is nothing to search is noise, and
    // that rule moved out of the body with the field.
    const services = fakeServices({
      open: vi.fn().mockResolvedValue({
        state: 'notARepository',
        bindingId: null,
        toplevel: null,
        envState: 'resolved',
        status: null,
      }),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    expect(panel.textContent).toContain('Not a git repository')
    expect(panel.querySelector('[data-testid="git-filter"]')).toBeNull()
  })

  it('typing part of a path leaves only the matching rows in BOTH lists, and clearing restores them', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: severalFiles })),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    const stagedList = () => panel.querySelector('[data-testid="git-staged-list"]')
    const unstagedList = () => panel.querySelector('[data-testid="git-unstaged-list"]')
    expect(stagedList()?.querySelectorAll('[role="listitem"]')).toHaveLength(2)
    expect(unstagedList()?.querySelectorAll('[role="listitem"]')).toHaveLength(3)

    // A directory component matches: the row renders the file name first and
    // its directory second (nocx-uf0p), so "git" must find src/git-panel.tsx.
    typeFilter(panel, 'git')
    await settle()
    // Staged has no match: the list is REPLACED by its empty state — a
    // filter that matches nothing is a state, never a blank.
    expect(stagedList()).toBeNull()
    expect(unstagedList()?.querySelectorAll('[role="listitem"]')).toHaveLength(1)
    // The row renders the name first and the directory second as SEPARATE
    // spans (nocx-uf0p); the filter matched the directory part of the path.
    expect(unstagedList()?.querySelector('.ui-file-status-row__name')?.textContent).toBe(
      'git-panel.tsx',
    )
    expect(unstagedList()?.querySelector('.ui-file-status-row__dir')?.textContent).toBe('src')

    // Case-insensitive: "A.TXT" finds the staged a.txt rows.
    typeFilter(panel, 'A.TXT')
    await settle()
    expect(stagedList()?.querySelectorAll('[role="listitem"]')).toHaveLength(1)
    expect(stagedList()?.textContent).toContain('a.txt')
    // No unstaged file contains "a.txt": its list is the empty state.
    expect(unstagedList()).toBeNull()

    // Clearing restores every row.
    typeFilter(panel, '')
    await settle()
    expect(stagedList()?.querySelectorAll('[role="listitem"]')).toHaveLength(2)
    expect(unstagedList()?.querySelectorAll('[role="listitem"]')).toHaveLength(3)
  })

  it('the section headings count the rows on screen — what matches, never the repository total', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: severalFiles })),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    expect(panel.textContent).toContain('Staged (2)')
    expect(panel.textContent).toContain('Unstaged (3)')
    typeFilter(panel, 'git')
    await settle()
    expect(panel.textContent).toContain('Unstaged (1)')
    expect(panel.textContent).not.toContain('Unstaged (3)')
    // The header keeps the wire's total — the repository's word, not the
    // list's — so a user still sees that 5 files are changed.
    expect(panel.textContent).toContain('5 changed')
  })

  it('filtering is renderer-side: typing never issues a scoped request per keystroke', async () => {
    const status = vi.fn().mockResolvedValue({ status: severalFiles })
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: severalFiles })),
      status,
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    const callsBefore = status.mock.calls.length

    typeFilter(panel, 'gi')
    typeFilter(panel, 'git')
    typeFilter(panel, 'git-')
    typeFilter(panel, '')
    await settle()

    // The open carried one status; the visible poll runs on a 5 s timer that
    // never fires in this test. Four keystrokes, zero extra requests.
    expect(status.mock.calls.length).toBe(callsBefore)
  })

  it('an empty result is a state, never a blank: each section says so and offers the one recovery', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: severalFiles })),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    typeFilter(panel, 'no-such-path')
    await settle()
    // The lists are gone; the sections say what happened instead.
    expect(panel.querySelector('[data-testid="git-staged-list"]')).toBeNull()
    expect(panel.querySelector('[data-testid="git-unstaged-list"]')).toBeNull()
    expect(panel.textContent).toContain('No staged files match')
    expect(panel.textContent).toContain('No unstaged files match')

    // The recovery is one click: clear the filter and the rows come back.
    const clearButtons = panel.querySelectorAll<HTMLElement>('[data-testid="git-filter-clear"]')
    expect(clearButtons.length).toBe(2)
    fireEvent.click(clearButtons[0])
    await settle()
    expect(panel.querySelector('[data-testid="git-staged-list"]')).not.toBeNull()
    expect(panel.textContent).not.toContain('No staged files match')
    expect(panel.textContent).toContain('Staged (2)')
  })

  it('a collapse and a filter compose: the filter never silently expands a section the user folded', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: severalFiles })),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()

    fireEvent.click(sectionDisclosure(panel, 'Unstaged'))
    await settle()
    expect(sectionDisclosure(panel, 'Unstaged').getAttribute('aria-expanded')).toBe('false')

    // A filter that matches rows inside the folded section changes nothing:
    // the section stays folded, and its heading shows the matching count.
    typeFilter(panel, 'git-panel')
    await settle()
    expect(sectionDisclosure(panel, 'Unstaged').getAttribute('aria-expanded')).toBe('false')
    expect(panel.textContent).toContain('Unstaged (1)')
    expect(panel.querySelector('[data-testid="git-unstaged-list"]')).toBeNull()
  })

  it('Escape clears the filter and keeps the focus', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: severalFiles })),
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    typeFilter(panel, 'git')
    await settle()
    expect(filterInput(panel).value).toBe('git')
    // The user is IN the field when they press Escape.
    filterInput(panel).focus()
    fireEvent.keyDown(filterInput(panel), { key: 'Escape' })
    await settle()
    expect(filterInput(panel).value).toBe('')
    expect(unstagedListText(panel)).toContain('notes.txt')
    // Escape dropped the filter, not the field: focus stays so the user can
    // keep typing.
    expect(document.activeElement).toBe(filterInput(panel))
  })

  it('a filter survives a view switch — the store outlives the panel (design §5.5)', async () => {
    const services = fakeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: severalFiles })),
      // The visibility effect polls the moment the panel is seen again; the
      // fixture repository has not changed, so the poll answers the same.
      status: vi.fn().mockResolvedValue({ status: severalFiles }),
    })
    const store = createGitStore(services)
    const [activeOrigin, setActiveOrigin] = createSignal<ActiveOrigin | null>(null)

    const first = mountApp(services, { store, origin: [activeOrigin, setActiveOrigin] })
    first.setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    typeFilter(first.panel, 'git')
    await settle()
    expect(first.panel.textContent).toContain('Unstaged (1)')

    // The view switch: the panel unmounts; the store lives on.
    first.handle.destroy()
    const second = mountApp(services, { store, origin: [activeOrigin, setActiveOrigin] })
    await settle()
    expect(filterInput(second.panel).value).toBe('git')
    expect(second.panel.textContent).toContain('Unstaged (1)')
    expect(second.panel.textContent).not.toContain('notes.txt')
  })

  it('a filter never crosses repositories: adopting a new binding clears it', async () => {
    const services = fakeServices({
      open: vi
        .fn()
        .mockResolvedValueOnce(openOk({ status: severalFiles })) // repo A
        .mockResolvedValueOnce(openOk({ bindingId: 'b2', toplevel: '/home/dev/other' })), // repo B
    })
    const { panel, setActiveOrigin } = mountApp(services)
    setActiveOrigin(LOCAL_ORIGIN)
    await settle()
    typeFilter(panel, 'git')
    await settle()
    expect(filterInput(panel).value).toBe('git')

    // The shell moves to another repository: the panel re-binds, and the
    // query typed against repo A must not hide repo B's files.
    setActiveOrigin(OTHER_ORIGIN)
    await settle()
    expect(filterInput(panel).value).toBe('')
  })
})

/** The unstaged list's text — the assertion the row-level tests reuse. */
function unstagedListText(panel: HTMLElement): string {
  return panel.querySelector('[data-testid="git-unstaged-list"]')?.textContent ?? ''
}
