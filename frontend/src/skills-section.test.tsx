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
    },
    {
      name: 'skill-authoring',
      description: 'Write useful skills',
      provenance: 'builtin',
      path: 'skill-authoring/SKILL.md',
      enabled: true,
    },
  ],
}

function fakeClient(overrides: Partial<SkillsClientLike> = {}): SkillsClientLike {
  return {
    list: vi.fn().mockResolvedValue(SKILLS),
    setEnabled: vi.fn().mockResolvedValue({ name: 'deploy', enabled: false }),
    remove: vi.fn().mockResolvedValue({ name: 'deploy' }),
    ...overrides,
  }
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
    const deploy = container.querySelector<HTMLElement>('[data-skill-name="deploy"]')!
    expect(deploy.textContent).toContain('/tmp/nocx/skills/deploy/SKILL.md')
    expect(deploy.textContent).toContain('authored')
    expect(container.querySelector('[data-skill-name="skill-authoring"]')).toBeTruthy()
    expect(container.querySelectorAll('button')).toHaveLength(1)
    fireEvent.click(deploy.querySelector('button')!)
    await waitFor(() => expect(remove).toHaveBeenCalledWith('deploy'))
    await waitFor(() => {
      expect(container.querySelector('[data-skill-name="deploy"]')).toBeNull()
    })
    expect(container.querySelector('[data-skill-name="skill-authoring"]')).toBeTruthy()
    expect(container.querySelector('[data-skill-name="skill-authoring"] button')).toBeNull()
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

    const toggle = container.querySelector<HTMLInputElement>(
      '[data-skill-name="deploy"] [role="switch"]',
    )!
    fireEvent.click(toggle)
    await waitFor(() => expect(setEnabled).toHaveBeenCalledWith('deploy', false))
    await waitFor(() => expect(toggle.checked).toBe(false))
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
