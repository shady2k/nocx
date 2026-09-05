// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SkillsSection } from './skills-section'
import { SkillsClient } from './skills-client'
import { SkillsStore, type SkillsClientLike } from './skills-store'
import type { Dispatcher } from './dispatcher'
import type { SkillsList } from './generated/skills.list'
import type { SkillsFile } from './generated/skills.file'
import type { SkillsFiles } from './generated/skills.files'

const confirmAnswer = true
// Only `showConfirm` is faked — the rest of the module is the real thing.
// The row no longer opens the kit's Dialog itself: reading a skill moved to
// its own tab (nocx-btg7d), and this file's Delete confirmation is the only
// surface of `./ui/dialog` it still touches.
vi.mock('./ui/dialog', async () => {
  const actual = await vi.importActual<typeof import('./ui/dialog')>('./ui/dialog')
  return { ...actual, showConfirm: () => Promise.resolve(confirmAnswer) }
})

/**
 * The row's Open button calls `openSkill` directly (nocx-54a2c) rather than
 * reading a file inline the way the deleted card did. `openSkill` refuses to
 * run before `registerSkillSurface` has wired it (skill-view/index.ts), which
 * this file — testing the ROW alone — never does, so the real function is
 * replaced with a spy that records what it was asked. skill-view's own
 * suites (open-skill.test.ts, skill-view-content.test.tsx) are what actually
 * exercise the tab this opens.
 */
const openSkillMock = vi.fn<(name: string) => void>()
vi.mock('./skill-view', () => ({ openSkill: (name: string) => openSkillMock(name) }))
afterEach(() => openSkillMock.mockClear())

/**
 * The one file every skill has. `skills.file` answers for any provenance —
 * builtin included, whose bytes are inside the binary and have no path on
 * disk at all, which is why the request is the skill's NAME plus a path
 * relative to the skill's own directory rather than the path the row prints.
 */
const BUILTIN_FILE: SkillsFile = {
  name: 'skill-authoring',
  path: 'SKILL.md',
  provenance: 'builtin',
  text: '---\nname: skill-authoring\n---\n\n# Write useful skills\n\nName the sentence first.\n',
  refusal: '',
  findings: [],
  maxBytes: 65536,
}

/**
 * `fakeClient`'s default answer for `skills.files` — this row no longer
 * reads a skill's manifest itself (that moved to the skill's own tab,
 * skill-view-body.tsx), but `SkillsClientLike` still requires the method, so
 * every `SkillsStore` built in this file needs something to return from it.
 */
const ONE_FILE: SkillsFiles = {
  name: 'skill-authoring',
  provenance: 'builtin',
  files: ['SKILL.md'],
  truncated: false,
  maxFiles: 256,
}

const SKILLS: SkillsList = {
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
    {
      name: 'skill-authoring',
      description: 'Write useful skills',
      provenance: 'builtin',
      path: 'skill-authoring/SKILL.md',
      enabled: true,
      status: 'approved',
    },
  ],
}

function fakeClient(overrides: Partial<SkillsClientLike> = {}): SkillsClientLike {
  return {
    // Refuses by default, and that is the point: an audit is a model call a
    // person asks for, so a fake that quietly answered would let a test about
    // the audit pass against a client that never ran one. The tests that are
    // about it pass their own.
    audit: vi.fn().mockRejectedValue(new Error('no audit was asked for in this test')),
    list: vi.fn().mockResolvedValue(SKILLS),
    setEnabled: vi.fn().mockResolvedValue({ name: 'deploy', enabled: false }),
    remove: vi.fn().mockResolvedValue({ name: 'deploy' }),
    approve: vi.fn().mockResolvedValue({ name: 'deploy', status: 'approved' }),
    file: vi.fn().mockResolvedValue(BUILTIN_FILE),
    files: vi.fn().mockResolvedValue(ONE_FILE),
    scan: vi.fn().mockRejectedValue(new Error('no scan was asked for in this test')),
    check: vi.fn().mockResolvedValue({ name: 'deploy', checked: false }),
    ...overrides,
  }
}

/**
 * Rows are located by the skill's VISIBLE NAME, through the kit's own row
 * identity — the way Connections, Endpoints and Snippets locate theirs.
 *
 * The old hand-built row carried a `data-skill-name` of its own; RecordRow
 * offers no per-row identity hook, and giving the surface one back would mean
 * wrapping each row in an element of its own. That is not free: `Stack divided
 * dense` draws the hairline and the row padding on its DIRECT children
 * (`.ui-stack[data-divided][data-dense] > *`, four selectors of specificity,
 * which is also what overrides the row's own padding), so a wrapper would take
 * the separator and the vertical rhythm while the row inside kept its own
 * gutter — doubled padding and an indent no other list in Settings has.
 *
 * The name is what a person reads to tell one row from another, so it is what
 * the test reads too (AGENTS.md testing rule 1).
 */
function rowFor(container: HTMLElement, name: string): HTMLElement | null {
  return (
    Array.from(container.querySelectorAll<HTMLElement>('.ui-collection-row')).find(
      (row) => row.querySelector('.ui-record-row__title')?.textContent === name,
    ) ?? null
  )
}

/**
 * A row's action by its ACCESSIBLE NAME, which since nocx-6jc4f is the only
 * name it has: the row's controls are icon buttons, so `textContent` is a
 * glyph and the thing a person is told the button does lives in `aria-label`.
 * The label names its row too ("Delete deploy"), so the match is on the verb
 * that opens it — which is what the caller here knows and what a screen
 * reader announces first.
 */
function actionIn(row: HTMLElement, label: string): HTMLButtonElement | undefined {
  return Array.from(row.querySelectorAll<HTMLButtonElement>('button')).find((button) => {
    const accessible = button.getAttribute('aria-label') ?? button.textContent ?? ''
    return accessible.trim() === label || accessible.trim().startsWith(`${label} `)
  })
}

describe('SkillsSection', () => {
  // Each test unmounts what it rendered — several tests in this file render
  // the same store's rows more than once, and a stray earlier render left in
  // the document is a second copy of every row `rowFor` could match.
  afterEach(cleanup)

  it('lists skill details and offers delete only for person-owned skills', async () => {
    const remove = vi.fn().mockResolvedValue({ name: 'deploy' })
    const client = fakeClient({
      list: vi
        .fn()
        .mockResolvedValueOnce(SKILLS)
        .mockResolvedValueOnce({
          ...SKILLS,
          skills: SKILLS.skills.filter((skill) => skill.name !== 'deploy'),
        }),
      remove,
    })
    const store = new SkillsStore(client)
    const { container } = render(() => <SkillsSection store={store} />)

    await waitFor(() => expect(screen.getByText('Deploy the service')).toBeTruthy())
    const deploy = rowFor(container, 'deploy')!
    expect(deploy.textContent).toContain('/tmp/nocx/skills/deploy/SKILL.md')
    expect(deploy.textContent).toContain('authored')
    const builtin = rowFor(container, 'skill-authoring')!
    expect(builtin).toBeTruthy()

    // A builtin ships inside the binary: there is nothing on disk to delete,
    // so its row simply has no Delete. The page used to spell that out in a
    // loose sentence under every builtin row; the absent button says it now,
    // which is why this asserts the two rows against each other rather than
    // counting the buttons on the page.
    expect(actionIn(builtin, 'Delete')).toBeUndefined()
    // Open is the one thing every row offers, builtin included — the one
    // control that changes nothing, so the absence of Delete is asserted
    // against what IS there rather than against an empty row (nocx-872jc.2).
    expect(actionIn(builtin, 'Open')).toBeTruthy()
    expect(builtin.querySelectorAll('button')).toHaveLength(1)
    const del = actionIn(deploy, 'Delete')
    expect(del).toBeTruthy()

    fireEvent.click(del!)
    await waitFor(() => expect(remove).toHaveBeenCalledWith('deploy'))
    await waitFor(() => {
      expect(rowFor(container, 'deploy')).toBeNull()
    })
    expect(rowFor(container, 'skill-authoring')).toBeTruthy()
    expect(actionIn(rowFor(container, 'skill-authoring')!, 'Delete')).toBeUndefined()
  })

  it('persists a toggle through the store and refreshes the returned state', async () => {
    const setEnabled = vi.fn().mockResolvedValue({ name: 'deploy', enabled: false })
    const client = fakeClient({
      setEnabled,
      list: vi
        .fn()
        .mockResolvedValueOnce(SKILLS)
        .mockResolvedValueOnce({
          ...SKILLS,
          skills: SKILLS.skills.map((skill) =>
            skill.name === 'deploy' ? { ...skill, enabled: false } : skill,
          ),
        }),
    })
    const store = new SkillsStore(client)
    const { container } = render(() => <SkillsSection store={store} />)
    await waitFor(() => expect(screen.getByText('Deploy the service')).toBeTruthy())

    const toggle = rowFor(container, 'deploy')!.querySelector<HTMLInputElement>('[role="switch"]')!
    fireEvent.click(toggle)
    await waitFor(() => expect(setEnabled).toHaveBeenCalledWith('deploy', false))
    await waitFor(() => expect(toggle.checked).toBe(false))
  })

  it('shows a changed managed skill with its path and offers re-approval', async () => {
    const approve = vi.fn().mockResolvedValue({ name: 'deploy', status: 'approved' })
    const changed: SkillsList = {
      ...SKILLS,
      skills: [{ ...SKILLS.skills[0], provenance: 'managed', status: 'changed' }],
    }
    const approved: SkillsList = {
      ...changed,
      skills: [{ ...changed.skills[0], status: 'approved' }],
    }
    const client = fakeClient({
      approve,
      list: vi.fn().mockResolvedValueOnce(changed).mockResolvedValueOnce(approved),
    })
    const store = new SkillsStore(client)
    const { container } = render(() => <SkillsSection store={store} />)

    // The status is read off the row that carries it, not off the page: the
    // kit's StatusDot renders the sentence twice — once visibly and once as
    // the accessible name — so a bare text query over the whole container
    // matches two nodes and cannot say which row is changed.
    await waitFor(() =>
      expect(
        rowFor(container, 'deploy')?.querySelector('.ui-record-row__status')?.textContent,
      ).toContain('Changed since installation'),
    )
    const deploy = rowFor(container, 'deploy')!
    expect(deploy.textContent).toContain('/tmp/nocx/skills/deploy/SKILL.md')
    fireEvent.click(actionIn(deploy, 'Re-approve')!)
    await waitFor(() => expect(approve).toHaveBeenCalledWith('deploy'))
    await waitFor(() => expect(container.textContent).not.toContain('Changed since installation'))
  })

  /** The evidence lines under a row's meta, in the order the row draws them. */
  const evidenceIn = (row: HTMLElement): string[] =>
    Array.from(row.querySelectorAll('.ui-record-row__detail > div')).map(
      (line) => line.textContent ?? '',
    )

  it('says where an installed skill came from, beside the file it is in', async () => {
    const store = new SkillsStore(fakeClient({ list: vi.fn().mockResolvedValue(INSTALLED) }))
    const { container } = render(() => <SkillsSection store={store} />)

    await waitFor(() => expect(rowFor(container, 'weather')).toBeTruthy())
    // BOTH, in that order: the file Delete acts on, and the address the bytes
    // came from. Either alone leaves a question the page is the only place to
    // ask — see the judgement in skills-section.tsx.
    // The second line is a SENTENCE and not a bare address (nocx-ojfuc.3): a
    // person reading two monospace strings should not have to work out what
    // the second one is a claim about. The address is still verbatim inside
    // it, which is the part that has to be.
    expect(evidenceIn(rowFor(container, 'weather')!)).toEqual([
      '/tmp/nocx/installed-skills/weather/SKILL.md',
      'Installed from https://example.com/weather/SKILL.md',
    ])
    // And nothing borrows it: a skill the person wrote has no source, and a
    // row that showed one would be claiming a stranger wrote their bytes.
    expect(evidenceIn(rowFor(container, 'deploy')!)).toEqual(['/tmp/nocx/skills/deploy/SKILL.md'])
  })

  it('draws no source line for a skill moved into the installed root by hand', async () => {
    const byHand: SkillsList = {
      ...SKILLS,
      skills: [
        {
          name: 'byhand',
          description: 'Put here with mv',
          provenance: 'installed',
          path: '/tmp/nocx/installed-skills/byhand/SKILL.md',
          enabled: true,
          status: 'approved',
        },
      ],
    }
    const store = new SkillsStore(fakeClient({ list: vi.fn().mockResolvedValue(byHand) }))
    const { container } = render(() => <SkillsSection store={store} />)

    await waitFor(() => expect(rowFor(container, 'byhand')).toBeTruthy())
    const row = rowFor(container, 'byhand')!
    // Installed, and nothing recorded: the row renders WITHOUT the line
    // rather than with an empty one. The provenance badge still says
    // installed, because the root decides that and not the document.
    expect(row.textContent).toContain('installed')
    expect(evidenceIn(row)).toEqual(['/tmp/nocx/installed-skills/byhand/SKILL.md'])
  })

  // Moved from the modal card's own audit tests (skills-section.test.tsx
  // before nocx-54a2c): the row now states what a stored check found on its
  // own third evidence line, since the card that used to be the only place
  // to read one is gone.
  it('shows a stored check as a third evidence line, dated and worded by the model, never a tick of our own', async () => {
    const checked: SkillsList = {
      ...SKILLS,
      skills: [
        {
          ...SKILLS.skills[0],
          check: { at: '2026-09-04T10:00:00Z', verdict: 'suspect', model: 'gemma-4-26b-a4b' },
        },
        SKILLS.skills[1],
      ],
    }
    const store = new SkillsStore(fakeClient({ list: vi.fn().mockResolvedValue(checked) }))
    const { container } = render(() => <SkillsSection store={store} />)
    await waitFor(() => expect(rowFor(container, 'deploy')).toBeTruthy())

    expect(evidenceIn(rowFor(container, 'deploy')!)).toEqual([
      '/tmp/nocx/skills/deploy/SKILL.md',
      'Checked 4 Sep — suspect',
    ])
    // A DATE AND THE MODEL'S WORD, NEVER A TICK OF OUR OWN: "✓ audited"
    // would read as an approval this fact never made — there is no approval
    // here, only what a model once said about the bytes.
    expect(rowFor(container, 'deploy')!.textContent).not.toContain('✓')
  })

  it('says the files have changed since a stored check, when the row already knows they moved', async () => {
    // `skill.check` carries no currency flag of its own (its own field
    // comment) — the row reads `skill.status` instead, which is the only
    // signal it already has for "the bytes moved since".
    const changed: SkillsList = {
      ...SKILLS,
      skills: [
        {
          ...SKILLS.skills[0],
          status: 'changed',
          check: { at: '2026-09-04T10:00:00Z', verdict: 'clear', model: 'gemma-4-26b-a4b' },
        },
      ],
    }
    const store = new SkillsStore(fakeClient({ list: vi.fn().mockResolvedValue(changed) }))
    const { container } = render(() => <SkillsSection store={store} />)
    await waitFor(() => expect(rowFor(container, 'deploy')).toBeTruthy())

    expect(evidenceIn(rowFor(container, 'deploy')!)).toEqual([
      '/tmp/nocx/skills/deploy/SKILL.md',
      'Checked 4 Sep, and the files have changed since',
    ])
  })

  it('draws the row\u2019s actions as icons, and Open reaches the skill\u2019s own tab (nocx-54a2c)', async () => {
    // WHAT A ROW OFFERS, and in what vocabulary. Icon buttons, one
    // vocabulary in one group, each keeping a name a screen reader can
    // read. The magnifier that used to sit here (nocx-6jc4f) opened the
    // card and started an audit in one press; it is gone now that a check
    // is remembered on the skill's own tab, and a second place to start one
    // would be a second owner of that press.
    const changed: SkillsList = {
      ...SKILLS,
      skills: [{ ...SKILLS.skills[0], status: 'changed' }],
    }
    const store = new SkillsStore(fakeClient({ list: vi.fn().mockResolvedValue(changed) }))
    const { container } = render(() => <SkillsSection store={store} />)
    await waitFor(() => expect(rowFor(container, 'deploy')).toBeTruthy())
    const row = rowFor(container, 'deploy')!

    // The busiest row the page can draw: everything it offers, named.
    const group = row.querySelector('.ui-action-group')!
    const buttons = Array.from(group.querySelectorAll('button'))
    expect(buttons.map((b) => b.getAttribute('aria-label'))).toEqual([
      'Open deploy',
      'Re-approve deploy',
      'Delete deploy',
    ])
    // Icons, not words: nothing in the group has a text label of its own,
    // and every one of them draws a glyph.
    for (const button of buttons) {
      expect(button.textContent?.trim()).toBe('')
      expect(button.querySelector('svg')).not.toBeNull()
    }

    // Open reaches the skill's own tab — `openSkill(name)` — and nothing
    // else: the row itself asks for no file, no manifest and no reading.
    fireEvent.click(actionIn(row, 'Open')!)
    expect(openSkillMock).toHaveBeenCalledWith('deploy')
  })

  it('puts every row\u2019s enable switch in the same place, whatever buttons the row carries', async () => {
    // The page\u2019s three row shapes in one list: a builtin (nothing to
    // delete, so no buttons at all), an authored skill (Delete), and a changed
    // one (Re-approve and Delete). The switch used to be the first child of
    // the action group, so its position was whatever the buttons after it
    // happened to be \u2014 three shapes, three positions, down a list a person
    // reads by scanning (nocx-xa0cq).
    //
    // Read STRUCTURALLY and not in pixels: jsdom lays nothing out, so every
    // getBoundingClientRect answers zeros and a coordinate assertion would
    // pass on the ragged page too. What holds the column is that the switch is
    // in the row\u2019s state cell, at the trailing edge, and never among the
    // actions \u2014 which is what is read here.
    const shapes: SkillsList = {
      ...SKILLS,
      skills: [
        SKILLS.skills[1],
        SKILLS.skills[0],
        { ...SKILLS.skills[0], name: 'managed', provenance: 'managed', status: 'changed' },
      ],
    }
    const store = new SkillsStore(fakeClient({ list: vi.fn().mockResolvedValue(shapes) }))
    const { container } = render(() => <SkillsSection store={store} />)
    await waitFor(() => expect(rowFor(container, 'managed')).toBeTruthy())

    // The shapes are asserted first, so a page that stopped drawing the
    // buttons could not make this test pass by having nothing to misalign.
    expect(actionIn(rowFor(container, 'skill-authoring')!, 'Delete')).toBeUndefined()
    expect(actionIn(rowFor(container, 'deploy')!, 'Delete')).toBeTruthy()
    expect(actionIn(rowFor(container, 'managed')!, 'Re-approve')).toBeTruthy()
    expect(actionIn(rowFor(container, 'managed')!, 'Delete')).toBeTruthy()

    for (const name of ['skill-authoring', 'deploy', 'managed']) {
      const row = rowFor(container, name)!
      const cell = row.querySelector('.ui-record-row__state')
      expect(cell, `${name} draws its switch in the row\u2019s state cell`).not.toBeNull()
      expect(cell?.querySelector('[role="switch"]')).not.toBeNull()
      expect(row.querySelector('.ui-action-group [role="switch"]')).toBeNull()
      // Last in the row\u2019s trailing region: the cell hangs off the row\u2019s
      // right edge, so nothing a particular row happens to offer can move it.
      expect(row.querySelector('.ui-collection-row__actions')?.lastElementChild).toBe(cell)
    }
  })

  it('shows a corrupt document as an actionable failure with its path', async () => {
    const result: SkillsList = {
      skills: [],
      documentPath: '/tmp/nocx/skills.json',
      documentError: 'parse skills.json: invalid character',
    }
    const store = new SkillsStore(fakeClient({ list: vi.fn().mockResolvedValue(result) }))
    render(() => <SkillsSection store={store} />)

    await waitFor(() => expect(screen.getByText(/Skills could not be read/)).toBeTruthy())
    expect(screen.getByText(/\/tmp\/nocx\/skills\.json/)).toBeTruthy()
  })
})

/**
 * THE PAGE MANAGES SKILLS AND DOES NOT ACQUIRE THEM (nocx-ojfuc.4, policy
 * design §5).
 *
 * The paste box, the classifier in front of it and the candidate picker are
 * gone. Acquisition is the assistant's `skills.install` tool: it searches,
 * follows a page to a repository, and the person decides in the approval
 * window, which names the resolved source, the description, the digest and
 * every file that would land with its bytes. Two surfaces owning one input is
 * the defect AGENTS.md names most often, and the one that went is the one
 * with a substitute.
 *
 * BOTH CHECKS ARE ABOUT ABSENCE, and each is written so that re-adding the
 * surface under another name fails it:
 *
 *   - No text-entry control anywhere on the page, in any state it can be in.
 *     A test naming `#skills-install-url` would pass against a box called
 *     anything else; this one passes only while there is nowhere on the page
 *     a person can type at all. The enable switches are `input` elements too,
 *     so the assertion is over the ones that TAKE TEXT.
 *   - No `skills.preview` and no `skills.install` on the wire, read off the
 *     dispatcher rather than off a hand-written fake. The client under it is
 *     the SHIPPED `SkillsClient`, which is also the check that the real
 *     client is still everything this page needs.
 */
const CHANGED: SkillsList = {
  ...SKILLS,
  skills: [
    ...SKILLS.skills,
    {
      name: 'weather',
      description: 'Answer questions about the weather',
      provenance: 'installed',
      path: '/tmp/nocx/installed-skills/weather/SKILL.md',
      enabled: false,
      // Changed, so the row carries every control it can carry: the status,
      // Re-approve, Delete, Open and the switch. A row in its quiet state
      // would leave the busiest half of the page unexercised.
      status: 'changed',
      source: { url: 'https://example.com/weather/SKILL.md', installedAt: '2026-09-03T12:00:00Z' },
    },
  ],
}

/** Nowhere to type. Every `input` the page draws is a switch; a text box of
 *  any kind, under any id or label, fails this. */
function textEntryIn(container: HTMLElement): Element[] {
  return Array.from(container.querySelectorAll('input, textarea, [contenteditable="true"]')).filter(
    (el) => !(el instanceof HTMLInputElement) || el.type !== 'checkbox',
  )
}

/** The shipped client over a dispatcher that records what it was asked. */
function recordingClient(answers: Record<string, unknown>): {
  client: SkillsClientLike
  methods: string[]
} {
  const methods: string[] = []
  const call = vi.fn((method: string) => {
    methods.push(method)
    return method in answers
      ? Promise.resolve(answers[method])
      : Promise.reject(new Error(`nothing answers ${method} in this test`))
  })
  return { client: new SkillsClient({ call } as unknown as Dispatcher), methods }
}

describe('SkillsSection — management only, no acquisition (nocx-ojfuc.4)', () => {
  afterEach(cleanup)

  it('offers nowhere to type a source address, in any state the page can be in', async () => {
    // Loading: the state the page opens in, before anything has answered.
    // The store is built OUTSIDE the component expression on purpose: `props`
    // is a getter, so a `new SkillsStore(...)` written inside the JSX is
    // evaluated afresh on every access — the subscription and the refresh
    // would land on two different stores and the page would never leave
    // "Loading skills".
    const loading = new SkillsStore(fakeClient({ list: () => new Promise<SkillsList>(() => {}) }))
    const pending = render(() => <SkillsSection store={loading} />)
    await waitFor(() => expect(pending.container.textContent).toContain('Loading skills'))
    expect(textEntryIn(pending.container)).toEqual([])
    cleanup()

    // Unreadable: the state that used to justify the box being on screen
    // regardless — "neither is a reason a person cannot install a skill".
    const unreadable = new SkillsStore(
      fakeClient({
        list: vi.fn().mockResolvedValue({
          skills: [],
          documentPath: '/tmp/nocx/skills.json',
          documentError: 'parse skills.json: invalid character',
        }),
      }),
    )
    const broken = render(() => <SkillsSection store={unreadable} />)
    await waitFor(() => expect(broken.container.textContent).toContain('Skills could not be read'))
    expect(textEntryIn(broken.container)).toEqual([])
    cleanup()

    // And with a list on screen, which is where the affordance used to sit.
    const listed = new SkillsStore(fakeClient({ list: vi.fn().mockResolvedValue(CHANGED) }))
    const { container } = render(() => <SkillsSection store={listed} />)
    await waitFor(() => expect(rowFor(container, 'weather')).toBeTruthy())
    expect(textEntryIn(container)).toEqual([])
    // And no control invites one under another name.
    const labels = Array.from(container.querySelectorAll('button')).map((b) =>
      (b.getAttribute('aria-label') ?? b.textContent ?? '').trim(),
    )
    expect(labels.length).toBeGreaterThan(0)
    for (const label of labels) {
      expect(label).not.toMatch(/url|address|paste|install|import|fetch/i)
    }
  })

  it('puts no acquisition call on the wire, whatever a person does with a row', async () => {
    const { client, methods } = recordingClient({
      'skills.list': CHANGED,
      'skills.setEnabled': { name: 'weather', enabled: true },
      'skills.approve': { name: 'weather', status: 'approved' },
      'skills.remove': { name: 'weather' },
    })
    const store = new SkillsStore(client)
    const { container } = render(() => <SkillsSection store={store} />)
    await waitFor(() => expect(rowFor(container, 'weather')).toBeTruthy())

    const row = () => rowFor(container, 'weather')!
    // Open reaches the skill's own tab, whose wire traffic is skill-view's
    // own suite to prove — nothing on THIS page's wire changes because of
    // it, which is what `openSkillMock` standing in for the real function
    // makes checkable at all.
    fireEvent.click(actionIn(row(), 'Open')!)
    expect(openSkillMock).toHaveBeenCalledWith('weather')

    fireEvent.click(row().querySelector<HTMLInputElement>('[role="switch"]')!)
    await waitFor(() => expect(methods).toContain('skills.setEnabled'))
    // Waiting for the CONTROL rather than for the call: a write marks its row
    // busy until the refresh behind it lands, so a click sent the moment the
    // method was recorded would land on a disabled button and assert nothing.
    await waitFor(() => expect(actionIn(row(), 'Re-approve')?.disabled).toBe(false))
    fireEvent.click(actionIn(row(), 'Re-approve')!)
    await waitFor(() => expect(methods).toContain('skills.approve'))
    await waitFor(() => expect(actionIn(row(), 'Delete')?.disabled).toBe(false))
    fireEvent.click(actionIn(row(), 'Delete')!)
    await waitFor(() => expect(methods).toContain('skills.remove'))

    // Every method this page put on the wire. The set is asserted whole
    // rather than by `not.toContain`s: a third acquisition method added
    // later would have to be named here, and so would a stray
    // `skills.files`/`skills.file`/`skills.audit` if the row ever grew a
    // second way to read a skill's bytes.
    expect(new Set(methods)).toEqual(
      new Set(['skills.list', 'skills.setEnabled', 'skills.approve', 'skills.remove']),
    )
  })
})

const INSTALLED: SkillsList = {
  ...SKILLS,
  skills: [
    ...SKILLS.skills,
    {
      name: 'weather',
      description: 'Answer questions about the weather',
      provenance: 'installed',
      path: '/tmp/nocx/installed-skills/weather/SKILL.md',
      // A freshly installed skill lands OFF (nocx-0bsa4.2): the bytes came
      // from outside, and the person turns it on after they have looked.
      enabled: false,
      status: 'approved',
      source: {
        url: 'https://example.com/weather/SKILL.md',
        installedAt: '2026-09-03T12:00:00Z',
        // What that address served, as the install recorded it. A different
        // value from anything else in this file on purpose: it is not the
        // hash of what is on disk, and a fixture that reused one would let a
        // test pass that confused the two.
        digest: 'c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00',
      },
    },
  ],
}
