// @vitest-environment jsdom
//
// SkillViewContent's BODY (nocx-4m1n1): the file list beside the file. Task
// 8's open-skill.test.ts already covers the header and the tab's lifecycle
// (resolution, closing, the switch); this suite is the body's own —
// mounted directly the way file-viewer-content.test.ts mounts its content,
// with a plain object standing in for PaneHost, rather than through a full
// PaneManager the body's own behaviour does not need.
//
// TWO CALLS, NOT ONE. `skills.files` is a bare directory listing (fast,
// unconditional) and `skills.scan` is the separate call that answers the
// live scan for the same bundle — folding the two together was the shape
// review rejected a second time: it made the file list, the tab's primary
// navigation, wait on reading and scanning the whole bundle before it could
// render at all. Every test below that exercises a dot builds it into the
// `skills.scan` FIXTURE, and the "no fan-out" test asserts `client.file` is
// called ONLY for the one file actually selected, never for the others.
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import type { PaneHost } from '../pane-content'
import { SkillsStore, type SkillsClientLike } from '../skills-store'
import type { SkillsList } from '../generated/skills.list'
import type { SkillsFile } from '../generated/skills.file'
import type { SkillsFiles } from '../generated/skills.files'
import type { SkillsScan } from '../generated/skills.scan'
import type { SkillsCheck } from '../generated/skills.check'
import type { SkillsAudit } from '../generated/skills.audit'
import { SkillViewContent } from './skill-view-content'

const A_SKILL: SkillsList['skills'][number] = {
  name: 'deploy',
  description: 'Deploy the service',
  provenance: 'authored',
  path: '/tmp/nocx/skills/deploy/SKILL.md',
  enabled: true,
  status: 'approved',
}

// 256, not a round fixture number — internal/skill/files.go's own
// MaxSkillFiles, and a stand-in for it here is exactly the drift a reviewer
// (rightly) does not have to trust: a test that says 64 is testing a cap
// nothing on the backend enforces.
const MAX_SKILL_FILES = 256

type ScanMatch = SkillsScan['matches'][number]
type Omission = SkillsScan['omitted'][number]

function scanMatch(path: string, count = 1): ScanMatch {
  return { path, count }
}

function omission(path: string, reason: Omission['reason']): Omission {
  return { path, reason }
}

function filesResult(
  files: readonly string[],
  overrides: Partial<Pick<SkillsFiles, 'truncated' | 'maxFiles'>> = {},
): SkillsFiles {
  return {
    name: 'deploy',
    provenance: 'authored',
    files: files as [string, ...string[]],
    truncated: false,
    maxFiles: MAX_SKILL_FILES,
    ...overrides,
  }
}

function scanResult(
  overrides: Partial<Pick<SkillsScan, 'read' | 'matches' | 'omitted' | 'maxBytes'>> = {},
): SkillsScan {
  return {
    name: 'deploy',
    provenance: 'authored',
    read: [],
    matches: [],
    omitted: [],
    maxBytes: 131072,
    ...overrides,
  }
}

function fileResult(overrides: Partial<SkillsFile> & { path: string }): SkillsFile {
  return {
    name: 'deploy',
    provenance: 'authored',
    text: '',
    refusal: '',
    maxBytes: 1_000_000,
    findings: [],
    ...overrides,
  }
}

/** A stored `skills.check.check` object — the shape `skills.check` and a
 *  fresh `skills.audit` both feed into the same rendering (nocx-dh14q). */
function checkFields(
  overrides: Partial<NonNullable<SkillsCheck['check']>> = {},
): NonNullable<SkillsCheck['check']> {
  return {
    provenance: 'authored',
    verdict: 'suspect',
    report: 'This skill asks scripts/setup.sh to reach the network.',
    role: 'auditing',
    endpoint: 'local',
    model: 'gemma-4-26b-a4b',
    digest: 'deadbeef',
    checkedAt: '2026-09-04T10:00:00Z',
    read: ['SKILL.md'],
    omitted: [],
    findings: [],
    maxBytes: 131072,
    ...overrides,
  }
}

/** `skills.check`'s own result — `null` for "nobody has checked this yet"
 *  (`checked: false`), or a stored `check` plus whether it is still current. */
function checkedResult(
  check: NonNullable<SkillsCheck['check']> | null,
  current = true,
): SkillsCheck {
  return check === null
    ? { name: 'deploy', checked: false }
    : { name: 'deploy', checked: true, check, current }
}

/** A `skills.audit` result — the shape a fresh Re-check press gets back. */
function auditFields(overrides: Partial<SkillsAudit> = {}): SkillsAudit {
  return {
    name: 'deploy',
    provenance: 'authored',
    role: 'auditing',
    endpoint: 'local',
    model: 'gemma-4-26b-a4b',
    verdict: 'clear',
    report: 'Nothing in these files reaches beyond what the skill describes.',
    read: ['SKILL.md'],
    omitted: [],
    maxBytes: 131072,
    findings: [],
    stored: 'yes',
    ...overrides,
  }
}

function fakeClient(overrides: Partial<SkillsClientLike> = {}): SkillsClientLike {
  return {
    list: vi.fn().mockResolvedValue({ documentPath: '/tmp/nocx/skills.json', skills: [A_SKILL] }),
    setEnabled: vi.fn().mockResolvedValue({ name: A_SKILL.name, enabled: false }),
    remove: vi.fn().mockResolvedValue({ name: A_SKILL.name }),
    approve: vi.fn().mockResolvedValue({ name: A_SKILL.name, status: 'approved' }),
    file: vi.fn().mockRejectedValue(new Error('not asked for in this suite')),
    files: vi.fn().mockRejectedValue(new Error('not asked for in this suite')),
    scan: vi.fn().mockResolvedValue(scanResult()),
    audit: vi.fn().mockRejectedValue(new Error('not asked for in this suite')),
    check: vi.fn().mockResolvedValue({ name: A_SKILL.name, checked: false }),
    ...overrides,
  }
}

// Enough microtask turns for one round of: the store's `list()` (setVisible's
// refresh), the body's `files()` and `scan()` (fired together), and the ONE
// on-demand `file()` a selection triggers.
async function flush(times = 12): Promise<void> {
  for (let i = 0; i < times; i++) await Promise.resolve()
}

/** A real PaneHost stub: `onStoreState` calls `setTitle` on every resolved
 *  skill, so `{} as PaneHost` (file-viewer-content.test.ts's stand-in) fails
 *  differently here — the call throws, `SkillsStore.refresh`'s `try` treats
 *  it as a failed `list()`, and the content sits in `unavailable` forever
 *  with no hint why. Named methods, not a cast, so that mistake cannot
 *  repeat silently. */
function fakePaneHost(): PaneHost {
  return {
    setTitle: vi.fn(),
    requestAttention: vi.fn(),
    requestClose: vi.fn(),
    contentSettled: vi.fn(),
  }
}

async function mount(
  client: SkillsClientLike,
): Promise<{ host: HTMLElement; content: SkillViewContent }> {
  const store = new SkillsStore(client)
  const content = new SkillViewContent('deploy', { store })
  const host = document.createElement('div')
  document.body.append(host)
  const signal = new AbortController().signal
  await content.mount(host, fakePaneHost(), signal)
  content.setVisible(true)
  await flush()
  return { host, content }
}

const fileListRows = (host: HTMLElement): HTMLElement[] =>
  Array.from(host.querySelectorAll('.skill-view__file-list .ui-record-row'))

const rowTitle = (row: HTMLElement): string =>
  row.querySelector('.ui-record-row__title')?.textContent ?? ''

const rowButton = (row: HTMLElement): HTMLButtonElement =>
  row.querySelector('.ui-record-row__open') as HTMLButtonElement

const viewCol = (host: HTMLElement): HTMLElement => host.querySelector('.skill-view__view-col')!

const viewText = (host: HTMLElement): string =>
  viewCol(host).querySelector('.ui-code-block')?.textContent ?? ''

const filePaths = (client: SkillsClientLike): string[] =>
  (client.file as Mock).mock.calls.map((call: unknown[]) => call[1] as string)

afterEach(() => {
  document.body.innerHTML = ''
})

describe('SkillViewContent — the bundle beside the file (nocx-4m1n1)', () => {
  it('lists every file of the bundle, including a bundle of one', async () => {
    // The modal card this tab replaces drew the list only when a skill had
    // MORE THAN ONE file (skills-section.tsx:691), so a one-file skill said
    // nothing at all about what it carried. The column's EXISTENCE is what
    // says the bundle has one file; its absence said nothing.
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'the whole skill' })),
    })
    const { host } = await mount(client)

    const rows = fileListRows(host)
    expect(rows).toHaveLength(1)
    expect(rowTitle(rows[0])).toBe('SKILL.md')
  })

  it('renders the list from `files` alone — it does not wait on `scan` to answer', async () => {
    // The whole reason the two calls are separate: a slow or stuck scan
    // must never hold the file list off the screen.
    let resolveScan: ((value: SkillsScan) => void) | undefined
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md', 'scripts/setup.sh'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      scan: vi.fn().mockImplementation(
        () =>
          new Promise<SkillsScan>((resolve) => {
            resolveScan = resolve
          }),
      ),
    })
    const { host } = await mount(client)

    expect(fileListRows(host)).toHaveLength(2)
    // The scan is still in flight — resolve it so the test does not leak a
    // pending promise into the next one.
    resolveScan?.(scanResult())
    await flush()
  })

  it('shows the bytes of the file that was chosen, and not of the one before it', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md', 'scripts/setup.sh'])),
      file: vi.fn().mockImplementation((_name: string, path: string) => {
        if (path === 'SKILL.md') return Promise.resolve(fileResult({ path, text: 'FIRST FILE' }))
        return Promise.resolve(fileResult({ path, text: 'SECOND FILE' }))
      }),
    })
    const { host } = await mount(client)

    // SKILL.md is first in the manifest and opens by default, the same
    // contract the modal card's SKILL_FILE constant relied on.
    expect(viewText(host)).toBe('FIRST FILE')

    const rows = fileListRows(host)
    const second = rows.find((row) => rowTitle(row) === 'scripts/setup.sh')
    if (!second) throw new Error('scripts/setup.sh did not render')
    rowButton(second).click()
    await flush()

    // Selecting the SECOND file changed what is shown, and the first
    // file's bytes are no longer on screen — asserted by doing the
    // selection, not by rendering one file and assuming the wiring holds.
    expect(viewText(host)).toBe('SECOND FILE')
    expect(viewText(host)).not.toContain('FIRST FILE')
  })

  it('reads a file on demand ONLY when it is selected — never a fan-out over the whole bundle', async () => {
    // The defect review caught: a first version read every listed file
    // through skills.file on open, which for MaxSkillFiles files is up to
    // 256 concurrent JSON-RPC calls against configSub's non-blocking
    // depth-8 semaphore (ws.go:1446,1640). `client.file` here must be
    // called for SKILL.md (the default selection) and for NOTHING ELSE,
    // however many files the manifest names.
    const client = fakeClient({
      files: vi
        .fn()
        .mockResolvedValue(
          filesResult(['SKILL.md', 'scripts/a.sh', 'scripts/b.sh', 'scripts/c.sh']),
        ),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
    })
    await mount(client)

    expect(filePaths(client)).toEqual(['SKILL.md'])
  })

  it("marks a scan-matched file with the kit's dot and names it, from the scan call alone", async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md', 'scripts/setup.sh'])),
      scan: vi.fn().mockResolvedValue(
        scanResult({
          read: ['SKILL.md', 'scripts/setup.sh'],
          matches: [scanMatch('scripts/setup.sh')],
        }),
      ),
      file: vi
        .fn()
        .mockImplementation((_name: string, path: string) =>
          Promise.resolve(fileResult({ path, text: 'x' })),
        ),
    })
    const { host } = await mount(client)

    const rows = fileListRows(host)
    const matched = rows.find((row) => rowTitle(row) === 'scripts/setup.sh')
    const clean = rows.find((row) => rowTitle(row) === 'SKILL.md')
    if (!matched || !clean) throw new Error('expected rows did not render')

    // The kit's dot, on the row the scan actually matched — never a bare
    // glyph (ui/README.md:383): StatusDot is what check-menu-icons and
    // nocx/no-raw-controls exist to require here.
    const dot = matched.querySelector('.ui-status-dot')
    expect(dot).not.toBeNull()
    expect(dot?.getAttribute('data-tone')).toBe('warning')
    expect(matched.querySelector('.ui-record-row__status')?.textContent).toContain(
      'scripts/setup.sh',
    )

    // The clean file carries no dot at all, once the scan has answered —
    // an empty `matches` entry is "nothing was found to match", not
    // "cleared", and drawing a dot for it would claim a fact the scan
    // never asserted.
    expect(clean.querySelector('.ui-status-dot')).toBeNull()

    // And this dot came from the SCAN call, not from reading the file:
    // only SKILL.md (the default selection) was ever read.
    expect(filePaths(client)).toEqual(['SKILL.md'])
  })

  it('marks every row PENDING while the scan has not answered yet — never nothing', async () => {
    // "not scanned yet" is one of three states a row can be in, and it must
    // be visibly distinct from "scanned, clean" (nothing drawn) — the
    // whole point of the two-call split's own review.
    let resolveScan: ((value: SkillsScan) => void) | undefined
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      scan: vi.fn().mockImplementation(
        () =>
          new Promise<SkillsScan>((resolve) => {
            resolveScan = resolve
          }),
      ),
    })
    const { host } = await mount(client)

    const row = fileListRows(host)[0]
    const dot = row.querySelector('.ui-status-dot')
    expect(dot).not.toBeNull()
    expect(dot?.getAttribute('data-tone')).toBe('neutral')
    expect(row.querySelector('.ui-record-row__status')?.textContent).toContain('pending')

    resolveScan?.(scanResult({ read: ['SKILL.md'] }))
    await flush()
    // Once the scan answers clean, the pending mark is gone — this is the
    // one state where an absent dot means something.
    expect(row.querySelector('.ui-status-dot')).toBeNull()
  })

  it('a file the scan could not read is marked, never left looking clean', async () => {
    // internal/skill/file.go:88's own rule, restated for the list: an
    // absent finding says nothing about a file either way, so a SKIPPED
    // file (too large, not text, unreadable, budget-spent) must draw
    // something other than nothing — the same "nothing" a clean, actually
    // scanned file draws.
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md', 'references/huge.md'])),
      scan: vi.fn().mockResolvedValue(
        scanResult({
          read: ['SKILL.md'],
          omitted: [omission('references/huge.md', 'too-large')],
        }),
      ),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
    })
    const { host } = await mount(client)

    const rows = fileListRows(host)
    const skipped = rows.find((row) => rowTitle(row) === 'references/huge.md')
    if (!skipped) throw new Error('references/huge.md did not render')

    const dot = skipped.querySelector('.ui-status-dot')
    expect(dot).not.toBeNull()
    // NEUTRAL, not warning: this is "we don't know", not "this matched" —
    // the two must not look alike, or a person reads a shrug as a finding.
    expect(dot?.getAttribute('data-tone')).toBe('neutral')
    expect(skipped.querySelector('.ui-record-row__status')?.textContent).toContain('not scanned')

    // And the skipped file was never itself read through skills.file either
    // — the scan already says why it has no match.
    expect(filePaths(client)).not.toContain('references/huge.md')
  })

  it('a scan that fails leaves every row pending, with a sentence saying so — never a clean-looking row', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      scan: vi.fn().mockRejectedValue(new Error('the scan could not be run')),
    })
    const { host } = await mount(client)

    const row = fileListRows(host)[0]
    const dot = row.querySelector('.ui-status-dot')
    expect(dot).not.toBeNull()
    expect(dot?.getAttribute('data-tone')).toBe('neutral')

    const notice = host.querySelector('.skill-view__list-col .ui-status-card[data-tone="warning"]')
    expect(notice).not.toBeNull()
    expect(notice?.textContent).toContain('the scan could not be run')
  })

  it("a file the scan's own walk never saw is pending, never clean — the race a review caught", async () => {
    // skills.files and skills.scan each walk the skill's directory at their
    // OWN moment, through separate calls: a file created in the gap is in
    // the frontend's file list but in neither the scan's `matches` nor its
    // `omitted`. Falling through to "clean" there would be the original
    // fan-out defect back again, arriving through a race instead. `read`
    // is what a viewer checks first: a path outside it was never examined
    // by THIS scan, whatever else is true about it.
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md', 'scripts/new.sh'])),
      // The scan's OWN walk only saw SKILL.md — as if scripts/new.sh
      // appeared after skills.scan's directory read but before this
      // component asked skills.files.
      scan: vi.fn().mockResolvedValue(scanResult({ read: ['SKILL.md'] })),
      file: vi
        .fn()
        .mockImplementation((_name: string, path: string) =>
          Promise.resolve(fileResult({ path, text: 'x' })),
        ),
    })
    const { host } = await mount(client)

    const rows = fileListRows(host)
    const raced = rows.find((row) => rowTitle(row) === 'scripts/new.sh')
    if (!raced) throw new Error('scripts/new.sh did not render')

    const dot = raced.querySelector('.ui-status-dot')
    expect(dot).not.toBeNull()
    expect(dot?.getAttribute('data-tone')).toBe('neutral')
    expect(raced.querySelector('.ui-record-row__status')?.textContent).toContain('pending')
  })

  it('a reactivation does not blank the file list or the view while the re-read is in flight', async () => {
    // The stall the two-call split exists to prevent, back again through
    // the refresh path: an earlier version reset `filesState`/`fileState`
    // to a placeholder on every `setVisible(true)`, so an already-open tab
    // showed nothing for the length of a round trip on every reactivation.
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'FIRST' })),
    })
    const { host, content } = await mount(client)
    expect(fileListRows(host)).toHaveLength(1)
    expect(viewText(host)).toBe('FIRST')

    // Freeze the SECOND round of files()/file() calls mid-flight.
    let resolveFiles: ((v: SkillsFiles) => void) | undefined
    let resolveFile: ((v: SkillsFile) => void) | undefined
    ;(client.files as Mock).mockImplementation(
      () =>
        new Promise<SkillsFiles>((resolve) => {
          resolveFiles = resolve
        }),
    )
    ;(client.file as Mock).mockImplementation(
      () =>
        new Promise<SkillsFile>((resolve) => {
          resolveFile = resolve
        }),
    )

    content.setVisible(false)
    content.setVisible(true)
    await flush()

    // Still the OLD list and the OLD bytes — nothing blanked while the
    // manifest re-read is in flight.
    expect(fileListRows(host)).toHaveLength(1)
    expect(rowTitle(fileListRows(host)[0])).toBe('SKILL.md')
    expect(viewText(host)).toBe('FIRST')

    resolveFiles?.(filesResult(['SKILL.md']))
    await flush()

    // The manifest answered and re-selected SKILL.md, which re-reads it —
    // still the OLD bytes on screen while THAT read is in flight too.
    expect(viewText(host)).toBe('FIRST')

    resolveFile?.(fileResult({ path: 'SKILL.md', text: 'FIRST' }))
    await flush()
  })

  it('never marks a dot from a stored check — only from skills.scan', async () => {
    // The row summary a stored skills.check would carry lives on `Skill`
    // via `skills.list`'s `check` field, and the Check group beside this
    // list legitimately calls `skills.check` of its own accord (nocx-dh14q)
    // — a different fact with a different source (design §3, §4). Neither
    // reaches the FILE LIST: nothing here reads `Skill.check` or
    // `skills.check`'s result, so a dot in this list can only ever be
    // sourced from skills.scan's own live answer.
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'clear' })),
      check: vi.fn().mockResolvedValue({ name: 'deploy', checked: false }),
    })
    const { host } = await mount(client)

    // The row's dot must be the pending-or-clean one, never a warning one
    // sourced from anywhere but skills.scan.
    expect(
      host.querySelector('.skill-view__file-list .ui-status-dot[data-tone="warning"]'),
    ).toBeNull()
  })

  it('surfaces the cut when the manifest is truncated, naming the cap', async () => {
    const client = fakeClient({
      files: vi
        .fn()
        .mockResolvedValue(
          filesResult(['SKILL.md'], { truncated: true, maxFiles: MAX_SKILL_FILES }),
        ),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
    })
    const { host } = await mount(client)

    expect(host.textContent).toContain(String(MAX_SKILL_FILES))
  })

  it('a manifest read that fails is drawn as a refusal, not an empty list', async () => {
    const client = fakeClient({
      files: vi.fn().mockRejectedValue(new Error('the directory could not be read')),
    })
    const { host } = await mount(client)

    expect(fileListRows(host)).toHaveLength(0)
    // Scoped to `data-tone="danger"` rather than the first status card in
    // the column: the "Check" placeholder above Files renders its own
    // (neutral) card in the same column.
    const notice = host.querySelector('.skill-view__list-col .ui-status-card[data-tone="danger"]')
    expect(notice).not.toBeNull()
    expect(notice?.textContent).toContain('the directory could not be read')
  })

  it('an on-demand file read that fails is drawn as a refusal, never as an empty clean file', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockRejectedValue(new Error('disk is gone')),
    })
    const { host } = await mount(client)

    // No code block at all — an empty block would be indistinguishable from
    // an empty FILE (FileReadout's own rule), and this is neither.
    expect(viewCol(host).querySelector('.ui-code-block')).toBeNull()
    const notice = viewCol(host).querySelector('.ui-status-card')
    expect(notice?.getAttribute('data-tone')).toBe('danger')
    expect(notice?.textContent).toContain('disk is gone')
  })

  it('re-reads the manifest, the scan, AND the file on screen on every activation, not only once', async () => {
    // The module comment's own claim: a tab "lives for days", so a re-read
    // on `setVisible(true)` — the same one SkillsStore.refresh() already
    // gets — must reach the body too, or a long-lived tab shows the bytes
    // and the marks exactly as they were the day it opened.
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
    })
    const { content } = await mount(client)

    const filesCalls = (client.files as Mock).mock.calls.length
    const scanCalls = (client.scan as Mock).mock.calls.length
    const fileCalls = (client.file as Mock).mock.calls.length
    expect(filesCalls).toBeGreaterThan(0)
    expect(scanCalls).toBeGreaterThan(0)
    expect(fileCalls).toBeGreaterThan(0)

    content.setVisible(false)
    content.setVisible(true)
    await flush()

    expect((client.files as Mock).mock.calls.length).toBeGreaterThan(filesCalls)
    expect((client.scan as Mock).mock.calls.length).toBeGreaterThan(scanCalls)
    expect((client.file as Mock).mock.calls.length).toBeGreaterThan(fileCalls)
  })

  it('↑/↓ move the roving focus, Enter opens the focused file and moves focus to the view', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md', 'scripts/setup.sh'])),
      file: vi
        .fn()
        .mockImplementation((_name: string, path: string) =>
          Promise.resolve(fileResult({ path, text: path === 'SKILL.md' ? 'FIRST' : 'SECOND' })),
        ),
    })
    const { host } = await mount(client)

    const list = host.querySelector('.skill-view__file-list') as HTMLElement
    const rows = fileListRows(host)
    rowButton(rows[0]).focus()
    expect(document.activeElement).toBe(rowButton(rows[0]))

    list.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    expect(document.activeElement).toBe(rowButton(rows[1]))

    list.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await flush()

    expect(viewText(host)).toBe('SECOND')
    const view = host.querySelector('.skill-view__view-col')
    expect(document.activeElement).toBe(view)
  })

  it('Enter on a file row shows the file even when a stored check made the pane default to the check', async () => {
    // Review round 3's finding A: the fixture for the test above defaults
    // `check` to `{checked:false}` (see `fakeClient`), which already
    // leaves `rightPane` at 'file' — so that test cannot see a keyboard
    // path that forgets to set it. A skill WITH a stored check opens on
    // the check pane by default; Enter on a file row must still switch to
    // the file, the same way clicking the row already does.
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md', 'scripts/setup.sh'])),
      file: vi
        .fn()
        .mockImplementation((_name: string, path: string) =>
          Promise.resolve(fileResult({ path, text: path === 'SKILL.md' ? 'FIRST' : 'SECOND' })),
        ),
      check: vi.fn().mockResolvedValue(checkedResult(checkFields())),
    })
    const { host } = await mount(client)

    // The check exists, so the pane opens on it by default.
    expect(host.querySelector('.skill-view__view-col .skill-view__check')).not.toBeNull()

    const list = host.querySelector('.skill-view__file-list') as HTMLElement
    const rows = fileListRows(host)
    rowButton(rows[0]).focus()
    list.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await flush()

    expect(viewText(host)).toBe('FIRST')
    expect(host.querySelector('.skill-view__view-col .skill-view__check')).toBeNull()
    const view = host.querySelector('.skill-view__view-col')
    expect(document.activeElement).toBe(view)
  })

  it("places the kit's ResizeHandle, bounded to [180px, 40% of the pane]", async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
    })
    const { host } = await mount(client)

    const handle = host.querySelector('.ui-resize-handle')
    expect(handle).not.toBeNull()
    expect(handle?.getAttribute('aria-valuemin')).toBe('180')
    const max = Number(handle?.getAttribute('aria-valuemax'))
    const now = Number(handle?.getAttribute('aria-valuenow'))
    expect(max).toBeGreaterThanOrEqual(180)
    expect(now).toBeGreaterThanOrEqual(180)
    expect(now).toBeLessThanOrEqual(max)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// The check pane (nocx-dh14q, Task 10): what content.db knows about this
// skill, shown beside the file list. `skills.check` is FREE and asked for on
// open; `skills.audit` is the one call that spends a model, and it runs only
// from the button in this pane — never from an effect, because
// internal/profile/role.go refuses to spend one silently.
// ═══════════════════════════════════════════════════════════════════════════
describe('SkillViewContent — the check pane (nocx-dh14q)', () => {
  // The panel now renders in the RIGHT PANE, selected the same way a file
  // is (review round 2: it was a fixed group in the narrow list column,
  // which is the defect this suite's own fix moved it out of). Every test
  // that inspects the panel's content selects the row first, exactly the
  // way a person reaches it — rather than depending on the default-pane
  // effect's race between the manifest and the stored check settling.
  const selectCheckRow = (host: HTMLElement): void => {
    const button = host.querySelector<HTMLButtonElement>('#skill-view-check .ui-record-row__open')
    if (!button) throw new Error('the Check row did not render')
    button.click()
  }

  const checkPane = (host: HTMLElement): HTMLElement | null =>
    host.querySelector('.skill-view__check')

  const findButton = (host: HTMLElement, label: string): HTMLButtonElement | undefined =>
    Array.from(host.querySelectorAll<HTMLButtonElement>('.skill-view__check .ui-button')).find(
      (b) => b.textContent?.includes(label),
    )

  it('THE CHECK is a row beside the files, and its panel renders in the RIGHT pane', async () => {
    // Review round 2's structural finding: an earlier shape stacked the
    // whole panel inside the narrow [180px, 40%] list column, above Files
    // — the modal's own defect (several components in one column)
    // reproduced narrower. The row lives in the left column; its content
    // renders in `.skill-view__view-col`, the same slot a file's bytes do.
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockResolvedValue(checkedResult(checkFields({ verdict: 'suspect' }))),
    })
    const { host } = await mount(client)

    // The row is in the LEFT column, and — because a check exists — it is
    // the default selection (design's testing note: "the check selected
    // by default when one exists").
    const row = host.querySelector('.skill-view__list-col #skill-view-check .ui-record-row')
    expect(row).not.toBeNull()
    expect(row?.textContent).toContain('Suspect')

    // The full panel — the verdict line, the prose — is in the RIGHT pane,
    // never inside the narrow list column.
    expect(host.querySelector('.skill-view__list-col .skill-view__check')).toBeNull()
    const panel = host.querySelector('.skill-view__view-col .skill-view__check')
    expect(panel).not.toBeNull()
    expect(panel?.textContent).toContain('Suspect')

    // Selecting a file switches the right pane back to the file's bytes.
    const fileRow = Array.from(host.querySelectorAll('.skill-view__file-list .ui-record-row')).find(
      (r) => r.querySelector('.ui-record-row__title')?.textContent === 'SKILL.md',
    )
    if (!fileRow) throw new Error('SKILL.md row did not render')
    fileRow.querySelector<HTMLButtonElement>('.ui-record-row__open')?.click()
    await flush()
    expect(host.querySelector('.skill-view__view-col .skill-view__check')).toBeNull()
    expect(host.querySelector('.skill-view__view-col .ui-code-block')).not.toBeNull()
  })

  it('defaults to the first file when there is no check to default to', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'file bytes' })),
      check: vi.fn().mockResolvedValue(checkedResult(null)),
    })
    const { host } = await mount(client)

    expect(host.querySelector('.skill-view__view-col .skill-view__check')).toBeNull()
    expect(host.querySelector('.skill-view__view-col .ui-code-block')?.textContent).toBe(
      'file bytes',
    )
  })

  it('does not yank a person off a file they already chose once a slow check finally answers', async () => {
    // Review round 3's finding B: `hasChosenDefaultPane` used to be set
    // ONLY by the default-pane effect. If `skills.check` is still `loading`
    // when the manifest settles — the same slow, write-queued content.db
    // finding 2 was about — the person sees a file, picks one explicitly,
    // and then the check lands `ready`: the effect re-runs, sees the flag
    // still false, and calls setRightPane('check'), discarding the choice.
    let resolveCheck: ((value: SkillsCheck) => void) | undefined
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md', 'scripts/setup.sh'])),
      file: vi
        .fn()
        .mockImplementation((_name: string, path: string) =>
          Promise.resolve(fileResult({ path, text: path === 'SKILL.md' ? 'FIRST' : 'SECOND' })),
        ),
      check: vi.fn().mockImplementation(
        () =>
          new Promise<SkillsCheck>((resolve) => {
            resolveCheck = resolve
          }),
      ),
    })
    const { host } = await mount(client)

    // The check has not answered yet, so nothing has been decided by the
    // default effect — the person is looking at whatever the pane starts
    // on. They explicitly pick the SECOND file.
    const rows = fileListRows(host)
    const second = rows.find((row) => rowTitle(row) === 'scripts/setup.sh')
    if (!second) throw new Error('scripts/setup.sh did not render')
    rowButton(second).click()
    await flush()
    expect(viewText(host)).toBe('SECOND')

    // The slow check finally answers, with a real stored reading.
    resolveCheck?.(checkedResult(checkFields()))
    await flush()

    // The person's explicit choice stands — the pane must not have been
    // yanked to the check just because it became available.
    expect(viewText(host)).toBe('SECOND')
    expect(host.querySelector('.skill-view__view-col .skill-view__check')).toBeNull()
  })

  it('asks what was concluded and spends nothing, on open', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockResolvedValue(checkedResult(null)),
    })
    await mount(client)

    // eslint-disable-next-line @typescript-eslint/unbound-method
    expect(client.check).toHaveBeenCalledWith('deploy')
    // eslint-disable-next-line @typescript-eslint/unbound-method
    expect(client.audit).not.toHaveBeenCalled()
  })

  it('with none: shows the Check this skill button and nothing else in that pane', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockResolvedValue(checkedResult(null)),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    const pane = checkPane(host)
    expect(pane).not.toBeNull()
    expect(findButton(host, 'Check this skill')).toBeDefined()
    // No verdict, no prose, no scan sentence — there is nothing stored yet.
    expect(pane?.textContent).not.toContain('Suspect')
    expect(pane?.textContent).not.toContain('Clear')
  })

  it('shows a stored verdict without calling a model again', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi
        .fn()
        .mockResolvedValue(
          checkedResult(
            checkFields({ verdict: 'suspect', model: 'gemma-4-26b-a4b', endpoint: 'local' }),
          ),
        ),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    const pane = checkPane(host)
    expect(pane?.textContent).toContain('Suspect')
    expect(pane?.textContent).toContain('gemma-4-26b-a4b')
    expect(pane?.textContent).toContain('local')
    expect(pane?.textContent).toContain('This skill asks scripts/setup.sh')
    // eslint-disable-next-line @typescript-eslint/unbound-method
    expect(client.audit).not.toHaveBeenCalled()
    // The button now offers a RE-check, not a first one.
    expect(findButton(host, 'Re-check')).toBeDefined()
    expect(findButton(host, 'Check this skill')).toBeUndefined()
  })

  it('says a stored check is about an earlier version when it no longer fits', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi
        .fn()
        .mockResolvedValue(checkedResult(checkFields({ report: 'The old report' }), false)),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    const pane = checkPane(host)
    // The stale sentence appears...
    expect(pane?.textContent).toContain('earlier version')
    // ...and the check itself is still shown in full, never hidden or
    // trimmed — a stale reading is still the reading.
    expect(pane?.textContent).toContain('The old report')
    expect(pane?.textContent).toContain('Suspect')
  })

  it("renders the model's prose inert: no markup, no live links", async () => {
    const hostile = '<script>alert(1)</script> and a [click here](javascript:alert(1)) link'
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockResolvedValue(checkedResult(checkFields({ report: hostile }))),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    const pane = checkPane(host)
    expect(pane).not.toBeNull()
    expect(pane?.querySelector('a[href]')).toBeNull()
    expect(pane?.innerHTML).not.toContain('<script')
    // The bytes are still readable, as inert text — not silently dropped.
    expect(pane?.textContent).toContain('alert(1)')
  })

  it('names the scan count apart from the verdict, with the files it was in', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md', 'scripts/setup.sh'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockResolvedValue(
        checkedResult(
          checkFields({
            findings: [
              {
                path: 'scripts/setup.sh',
                patternId: 'curl-pipe-shell',
                line: 'curl | sh',
                lineNumber: 4,
              },
            ],
          }),
        ),
      ),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    const pane = checkPane(host)
    expect(pane?.textContent).toContain('matched 1 line')
    expect(pane?.textContent).toContain('scripts/setup.sh')
    // "absence of a match is not safety" stays even when the scan DID
    // match something (review round 2's minor: it used to disappear the
    // moment findings.length > 0, leaving the other, CLEAN files in the
    // same reading unvouched-for with nothing saying so).
    expect(pane?.textContent).toContain('not the same as safe')
  })

  it('names what was left out of a stored reading, as a sentence', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi
        .fn()
        .mockResolvedValue(
          checkedResult(
            checkFields({ omitted: [{ path: 'references/huge.md', reason: 'too-large' }] }),
          ),
        ),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    expect(checkPane(host)?.textContent).toContain('references/huge.md')
  })

  it('says the scan count is a fact about what was read THEN, on a stale check', async () => {
    // Review round 2's minor: the scan sentence was present tense even on
    // a stale check, claiming about bytes that may no longer be on disk.
    // Design §4 says the stored count is a fact about what was read then.
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockResolvedValue(checkedResult(checkFields(), false)),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    expect(checkPane(host)?.textContent).toContain('When this reading was made')
  })

  it('shows the report even when it could not be saved', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockResolvedValue(checkedResult(null)),
      audit: vi.fn().mockResolvedValue(
        auditFields({
          report: 'A fresh reading',
          stored: 'no',
          storedError: 'the store is a stub',
        }),
      ),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    const button = findButton(host, 'Check this skill')
    if (!button) throw new Error('Check this skill button did not render')
    button.click()
    await flush()

    const pane = checkPane(host)
    expect(pane?.textContent).toContain('A fresh reading')
    expect(pane?.textContent).toContain('not saved')
    expect(pane?.textContent).toContain('the store is a stub')
  })

  it('offers no check on a builtin: no button, no panel, no call', async () => {
    const builtin: SkillsList['skills'][number] = { ...A_SKILL, provenance: 'builtin' }
    const client = fakeClient({
      list: vi.fn().mockResolvedValue({ documentPath: '/tmp/nocx/skills.json', skills: [builtin] }),
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
    })
    const { host } = await mount(client)

    expect(checkPane(host)).toBeNull()
    // eslint-disable-next-line @typescript-eslint/unbound-method
    expect(client.check).not.toHaveBeenCalled()
  })

  it('a check that could not be read is shown as a refusal, and still offers the button', async () => {
    // Review round 2's Important #2: `skills.check` errors only on a
    // genuine store fault (a stub or unwired store answers `checked:
    // false` instead) — so a READ failure here is not a reason to
    // withhold `skills.audit`, which would run happily and report
    // `stored:'no'` per design §6. An earlier version rendered no button
    // at all in this branch.
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockRejectedValue(new Error('content.db is locked')),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    expect(checkPane(host)?.textContent).toContain('content.db is locked')
    const button = findButton(host, 'Check this skill')
    expect(button).toBeDefined()
    expect(button?.disabled).toBe(false)
  })

  it('a reading that fails leaves the button available again, with a sentence saying why', async () => {
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockResolvedValue(checkedResult(null)),
      audit: vi.fn().mockRejectedValue(new Error('the model refused')),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    const button = findButton(host, 'Check this skill')
    if (!button) throw new Error('button did not render')
    button.click()
    await flush()

    expect(checkPane(host)?.textContent).toContain('the model refused')
    const again = findButton(host, 'Check this skill')
    expect(again?.disabled).toBe(false)
  })

  it('Re-check calls skills.audit exactly once per press and is disabled while it is in flight', async () => {
    let resolveAudit: ((value: SkillsAudit) => void) | undefined
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockResolvedValue(checkedResult(checkFields())),
      audit: vi.fn().mockImplementation(
        () =>
          new Promise<SkillsAudit>((resolve) => {
            resolveAudit = resolve
          }),
      ),
    })
    const { host } = await mount(client)
    selectCheckRow(host)

    const button = findButton(host, 'Re-check')
    if (!button) throw new Error('button did not render')
    button.click()
    await flush()

    expect(button.disabled).toBe(true)
    // A second press while the first is still in flight must not ask again.
    button.click()
    await flush()
    // eslint-disable-next-line @typescript-eslint/unbound-method
    expect(client.audit).toHaveBeenCalledTimes(1)

    resolveAudit?.(auditFields())
    await flush()
    expect(button.disabled).toBe(false)
  })

  it('a slow reactivation read cannot clobber a fresh Re-check result', async () => {
    // Review round 2's Important #1: content.db is single-connection and
    // reads queue behind writes, so a `loadCheck` from a reactivation can
    // still be in flight when a Re-check press resolves. Without bumping
    // `checkGeneration` at the top of `runAudit`, that stale read would
    // land AFTER the fresh reading and silently overwrite it — the report
    // the person was just billed for, gone with no trace.
    let resolveSecondCheck: ((value: SkillsCheck) => void) | undefined
    let checkCalls = 0
    const client = fakeClient({
      files: vi.fn().mockResolvedValue(filesResult(['SKILL.md'])),
      file: vi.fn().mockResolvedValue(fileResult({ path: 'SKILL.md', text: 'x' })),
      check: vi.fn().mockImplementation(() => {
        checkCalls += 1
        if (checkCalls === 1) {
          return Promise.resolve(checkedResult(checkFields({ verdict: 'suspect' })))
        }
        // The reactivation's own read — held open until the test resolves
        // it, standing in for a slow content.db queued behind a write.
        return new Promise<SkillsCheck>((resolve) => {
          resolveSecondCheck = resolve
        })
      }),
      audit: vi
        .fn()
        .mockResolvedValue(auditFields({ verdict: 'clear', report: 'Fresh audit report' })),
    })
    const { host, content } = await mount(client)
    selectCheckRow(host)
    expect(checkPane(host)?.textContent).toContain('Suspect')

    // Reactivation: bumps refreshToken, fires the SECOND (slow) skills.check.
    content.setVisible(false)
    content.setVisible(true)
    await flush()

    // Review round 3's minor: `resolveSecondCheck` is a no-op if the
    // reactivation never made a second call, which would let this test
    // pass vacuously — green whether or not a reactivation still re-reads
    // the check at all. Pin the premise before the press.
    expect(checkCalls).toBe(2)

    // Re-check is pressed WHILE that slow read is still in flight.
    const button = findButton(host, 'Re-check')
    if (!button) throw new Error('button did not render')
    button.click()
    await flush()

    expect(checkPane(host)?.textContent).toContain('Fresh audit report')

    // The stale reactivation read finally lands...
    resolveSecondCheck?.(
      checkedResult(checkFields({ verdict: 'suspect', report: 'The old report' })),
    )
    await flush()

    // ...and must not have clobbered the fresh audit result.
    expect(checkPane(host)?.textContent).toContain('Fresh audit report')
    expect(checkPane(host)?.textContent).not.toContain('The old report')
  })
})
