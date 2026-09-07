import { describe, expect, it, vi, type Mock } from 'vitest'
import { SkillsStore, type SkillsClientLike } from './skills-store'
import type { SkillsList } from './generated/skills.list'
import type { SkillsFile } from './generated/skills.file'
import type { SkillsFiles } from './generated/skills.files'
import type { SkillsScan } from './generated/skills.scan'

const SKILLS: SkillsList = {
  refused: [],
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
}

const A_FILE: SkillsFile = {
  name: 'deploy',
  path: 'SKILL.md',
  provenance: 'authored',
  text: '---\nname: deploy\n---\n',
  refusal: '',
  findings: [],
  maxBytes: 65536,
}

const A_MANIFEST: SkillsFiles = {
  name: 'deploy',
  provenance: 'authored',
  files: ['SKILL.md'],
  truncated: false,
  maxFiles: 256,
}

const A_SCAN: SkillsScan = {
  name: 'deploy',
  provenance: 'authored',
  read: ['SKILL.md'],
  matches: [],
  omitted: [],
  maxBytes: 131072,
}

function fakeClient(overrides: Partial<SkillsClientLike> = {}): SkillsClientLike {
  return {
    audit: vi.fn().mockRejectedValue(new Error('no audit was asked for in this test')),
    list: vi.fn().mockResolvedValue(SKILLS),
    setEnabled: vi.fn().mockResolvedValue({ name: 'deploy', enabled: false }),
    remove: vi.fn().mockResolvedValue({ name: 'deploy' }),
    approve: vi.fn().mockResolvedValue({ name: 'deploy', status: 'approved' }),
    file: vi.fn().mockResolvedValue(A_FILE),
    files: vi.fn().mockResolvedValue(A_MANIFEST),
    scan: vi.fn().mockResolvedValue(A_SCAN),
    check: vi.fn().mockResolvedValue({ name: 'deploy', checked: false }),
    ...overrides,
  }
}

describe('SkillsStore.check', () => {
  it('reading a check refreshes nothing', async () => {
    // skills.check writes nothing, so a list that changed after one would
    // have changed for a reason nobody can name — the rule `file` and
    // `files` already keep.
    const client = fakeClient()
    const store = new SkillsStore(client)
    await store.refresh()
    const listCalls = (client.list as Mock).mock.calls.length
    await store.check('deploy')
    expect((client.list as Mock).mock.calls.length).toBe(listCalls)
  })
})

describe('SkillsStore.scan', () => {
  it('reading a scan refreshes nothing', async () => {
    // skills.scan writes nothing either, for the same reason.
    const client = fakeClient()
    const store = new SkillsStore(client)
    await store.refresh()
    const listCalls = (client.list as Mock).mock.calls.length
    await store.scan('deploy')
    expect((client.list as Mock).mock.calls.length).toBe(listCalls)
  })
})
