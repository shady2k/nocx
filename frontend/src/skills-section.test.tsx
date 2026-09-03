// @vitest-environment jsdom
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { SkillsSection } from './skills-section'
import { SkillsStore, type SkillsClientLike } from './skills-store'
import type { SkillsList } from './generated/skills.list'

const confirmAnswer = true
vi.mock('./ui/dialog', () => ({
  showConfirm: () => Promise.resolve(confirmAnswer),
}))

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
    list: vi.fn().mockResolvedValue(SKILLS),
    setEnabled: vi.fn().mockResolvedValue({ name: 'deploy', enabled: false }),
    remove: vi.fn().mockResolvedValue({ name: 'deploy' }),
    approve: vi.fn().mockResolvedValue({ name: 'deploy', status: 'approved' }),
    preview: vi.fn().mockResolvedValue({
      name: 'deploy',
      description: 'Deploy the service',
      body: 'body',
      url: 'https://example.com/SKILL.md',
      findings: [],
    }),
    install: vi.fn().mockResolvedValue({ name: 'deploy', provenance: 'installed' }),
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

/** A row's action by its visible label — the accessible name a person clicks. */
function actionIn(row: HTMLElement, label: string): HTMLButtonElement | undefined {
  return Array.from(row.querySelectorAll<HTMLButtonElement>('button')).find(
    (button) => button.textContent?.trim() === label,
  )
}

describe('SkillsSection', () => {
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
    expect(builtin.querySelectorAll('button')).toHaveLength(0)
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
      ).toContain('Changed since approval'),
    )
    const deploy = rowFor(container, 'deploy')!
    expect(deploy.textContent).toContain('/tmp/nocx/skills/deploy/SKILL.md')
    fireEvent.click(actionIn(deploy, 'Re-approve')!)
    await waitFor(() => expect(approve).toHaveBeenCalledWith('deploy'))
    await waitFor(() => expect(container.textContent).not.toContain('Changed since approval'))
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
