/**
 * SkillsStore.install — the renderer's end of "adopt what I just read".
 *
 * The ordering is the thing worth pinning: the list is refreshed BEFORE the
 * promise resolves, so a dialog that closes on this promise cannot close over
 * a list that has not caught up and leave the person looking at a Skills page
 * without the skill they just installed in it.
 */
import { describe, expect, it, vi } from 'vitest'
import { SkillsStore } from './skills-store'
import type { SkillsClientLike, SkillsState } from './skills-store'

function fakeClient(installed: string[]) {
  const order: string[] = []
  const install = vi.fn(() => {
    order.push('install')
    installed.push('deploy')
    return Promise.resolve({ name: 'deploy', provenance: 'installed' as const })
  })
  const client: SkillsClientLike = {
    list: vi.fn(() => {
      order.push('list')
      return Promise.resolve({
        skills: installed.map((name) => ({
          name,
          description: 'Deploy the service',
          provenance: 'installed' as const,
          path: `/config/installed-skills/${name}/SKILL.md`,
          enabled: true,
          status: 'approved' as const,
        })),
        documentPath: '/config/skills.json',
      })
    }),
    setEnabled: vi.fn().mockResolvedValue(undefined),
    remove: vi.fn().mockResolvedValue(undefined),
    approve: vi.fn().mockResolvedValue(undefined),
    preview: vi.fn(),
    install,
    file: vi.fn(),
  }
  return { client, order, install }
}

describe('SkillsStore.install', () => {
  it('installs, refreshes the list, and answers with what was installed', async () => {
    const { client, order, install } = fakeClient([])
    const store = new SkillsStore(client)
    const seen: SkillsState[] = []
    store.subscribe((state) => seen.push(state))

    const result = await store.install('https://example.com/skills/deploy/SKILL.md')

    expect(install).toHaveBeenCalledWith('https://example.com/skills/deploy/SKILL.md')
    expect(result).toEqual({ name: 'deploy', provenance: 'installed' })
    // The refresh happened, and it happened after the install rather than
    // racing it.
    expect(order).toEqual(['install', 'list'])
    const last = seen[seen.length - 1]
    expect(last.kind).toBe('ready')
    if (last.kind === 'ready') {
      expect(last.skills.map((skill) => skill.name)).toEqual(['deploy'])
    }
  })

  it('lets a refusal through and leaves the list alone', async () => {
    const { client, order } = fakeClient(['already-here'])
    client.install = vi.fn().mockRejectedValue(new Error('that skill could not be fetched: 404'))
    const store = new SkillsStore(client)

    await expect(store.install('https://example.com/gone')).rejects.toThrow('could not be fetched')
    expect(order).toEqual([])
  })
})
