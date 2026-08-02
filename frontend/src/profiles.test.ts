import { describe, it, expect, vi } from 'vitest'
import {
  adoptAliasProfile,
  buildGroupTree,
  resolveGroupPath,
  parseQuickConnect,
  ProfileClient,
  type EffectiveFieldDTO,
  type PatchParams,
  type SSHProfile,
  type SSHAliasResponse,
} from './profiles'
import { Dispatcher } from './dispatcher'
import type { ProfileGroup } from './profiles'

describe('buildGroupTree', () => {
  it('builds a flat tree from nested groups via parentGroupId', () => {
    const groups: ProfileGroup[] = [
      { id: 'g1', name: 'Prod' },
      { id: 'g2', name: 'Staging', parentGroupId: 'g1' },
      { id: 'g3', name: 'web-1', parentGroupId: 'g2' },
      { id: 'g4', name: 'Orphan' },
    ]
    const roots = buildGroupTree(groups)
    expect(roots).toHaveLength(2)
    const prod = roots.find((r) => r.id === 'g1')
    expect(prod).toBeDefined()
    expect(prod!.children).toHaveLength(1)
    expect(prod!.children[0].id).toBe('g2')
    expect(prod!.children[0].children).toHaveLength(1)
    expect(prod!.children[0].children[0].id).toBe('g3')
  })

  it('orphaned groups become roots', () => {
    const groups: ProfileGroup[] = [
      { id: 'g1', name: 'A' },
      { id: 'g2', name: 'B', parentGroupId: 'nonexistent' },
    ]
    const roots = buildGroupTree(groups)
    expect(roots).toHaveLength(2)
  })
})

describe('resolveGroupPath', () => {
  it('walks parent chain returning breadcrumb names', () => {
    const groups: ProfileGroup[] = [
      { id: 'g1', name: 'Prod' },
      { id: 'g2', name: 'Staging', parentGroupId: 'g1' },
      { id: 'g3', name: 'web-1', parentGroupId: 'g2' },
    ]
    const path = resolveGroupPath(groups, 'g3')
    expect(path).toEqual(['Prod', 'Staging', 'web-1'])
  })

  it('returns single-element path for root group', () => {
    const groups: ProfileGroup[] = [{ id: 'g1', name: 'Prod' }]
    const path = resolveGroupPath(groups, 'g1')
    expect(path).toEqual(['Prod'])
  })

  it('cycle-guards at 32 levels', () => {
    const groups: ProfileGroup[] = [
      { id: 'g1', name: 'A', parentGroupId: 'g2' },
      { id: 'g2', name: 'B', parentGroupId: 'g1' },
    ]
    const path = resolveGroupPath(groups, 'g1')
    expect(path.length).toBeGreaterThan(0)
    expect(path.length).toBeLessThanOrEqual(32)
  })
})

describe('parseQuickConnect', () => {
  it('parses user@host:port', () => {
    const p = parseQuickConnect('alice@example.com:2222')
    expect(p.options.user).toBe('alice')
    expect(p.options.host).toBe('example.com')
    expect(p.options.port).toBe(2222)
  })

  it('parses user@host with default port 22', () => {
    const p = parseQuickConnect('alice@example.com')
    expect(p.options.user).toBe('alice')
    expect(p.options.host).toBe('example.com')
    expect(p.options.port).toBe(22)
  })

  it('parses bare host with default user', () => {
    const p = parseQuickConnect('example.com')
    expect(p.options.host).toBe('example.com')
    expect(p.options.port).toBe(22)
  })

  it('parses [host]:port for IPv6', () => {
    const p = parseQuickConnect('[::1]:2222')
    expect(p.options.host).toBe('::1')
    expect(p.options.port).toBe(2222)
  })

  it('parses ssh://user@host:port', () => {
    const p = parseQuickConnect('ssh://deploy@10.0.0.1:2222')
    expect(p.options.user).toBe('deploy')
    expect(p.options.host).toBe('10.0.0.1')
    expect(p.options.port).toBe(2222)
  })

  it('parses ssh://host with default port', () => {
    const p = parseQuickConnect('ssh://example.com')
    expect(p.options.host).toBe('example.com')
    expect(p.options.port).toBe(22)
    expect(p.options.user).toBeUndefined()
  })
})

describe('SSHProfile shape', () => {
  it('has the expected secret binding fields', () => {
    const p: SSHProfile = {
      id: 'ssh:custom:test:0001',
      type: 'ssh',
      name: 'test',
      group: '',
      options: {
        host: 'example.com',
        port: 22,
        passwordSecret: 'secrow:abc',
      },
    }
    expect(p.type).toBe('ssh')
    expect(p.options.passwordSecret).toBe('secrow:abc')
  })
})

describe('EffectiveProfile types', () => {
  it('stores per-field effective values with closed-enum source kinds', () => {
    const field: EffectiveFieldDTO = {
      value: 2222,
      source: { kind: 'group', id: 'g1', label: 'Prod' },
    }
    expect(field.source.kind).toMatch(/^(profile|group|sshConfig|global|default)$/)
    expect(field.value).toBe(2222)
    expect(field.source.label).toBe('Prod')
  })

  it('profile source kind has no id/label', () => {
    const field: EffectiveFieldDTO = {
      value: 'my-host',
      source: { kind: 'profile', id: '', label: '' },
    }
    expect(field.source.kind).toBe('profile')
  })

  it('sshConfig source kind has no id', () => {
    const field: EffectiveFieldDTO = {
      value: 22,
      source: { kind: 'sshConfig', id: '', label: '' },
    }
    expect(field.source.kind).toBe('sshConfig')
  })
})

describe('PatchParams', () => {
  it('accepts set and unset as disjoint operations', () => {
    const params: PatchParams = {
      id: 'prof:ssh:my-server',
      set: { 'options.port': 2222 },
      unset: ['options.user'],
    }
    expect(params.set!['options.port']).toBe(2222)
    expect(params.unset).toContain('options.user')
  })

  it('accepts unset-only revert operation', () => {
    const params: PatchParams = {
      id: 'prof:ssh:my-server',
      unset: ['options.port'],
    }
    expect(params.set).toBeUndefined()
    expect(params.unset).toHaveLength(1)
  })

  it('accepts set-only override', () => {
    const params: PatchParams = {
      id: 'prof:ssh:my-server',
      set: { 'options.port': 2222 },
    }
    expect(params.unset).toBeUndefined()
    expect(params.set!['options.port']).toBe(2222)
  })
})

describe('importSSHConfig', () => {
  const MOCK_ALIASES: SSHAliasResponse = {
    aliases: [
      { alias: 'prod-db', hostName: '10.0.0.1', user: 'deploy', port: 2222 },
      { alias: 'dev-box', hostName: 'dev.local', user: 'dev' },
      { alias: 'staging', hostName: 'staging.example.com' },
    ],
    unavailable: null,
  }

  const mockProfile = (name: string, host: string): SSHProfile => ({
    id: `prof:ssh:${name}`,
    type: 'ssh',
    name,
    options: { host },
  })

  it('imports all aliases when no collisions exist', async () => {
    const pc = new ProfileClient(new Dispatcher())
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue(MOCK_ALIASES)
    vi.spyOn(pc, 'listProfiles').mockResolvedValue([])
    const createSpy = vi.spyOn(pc, 'createProfile').mockResolvedValue(mockProfile('', ''))

    const result = await pc.importSSHConfig()
    expect(result.profilesImported).toBe(3)
    expect(result.skipped).toBe(0)
    expect(createSpy).toHaveBeenCalledTimes(3)
  })

  it('skips aliases whose name already exists in saved profiles', async () => {
    const pc = new ProfileClient(new Dispatcher())
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue(MOCK_ALIASES)
    vi.spyOn(pc, 'listProfiles').mockResolvedValue([mockProfile('prod-db', 'different-host')])
    const createSpy = vi.spyOn(pc, 'createProfile').mockResolvedValue(mockProfile('', ''))

    const result = await pc.importSSHConfig()
    expect(result.profilesImported).toBe(2)
    expect(result.skipped).toBe(1)
    expect(createSpy).toHaveBeenCalledTimes(2)
  })

  it('skips aliases whose host already exists in saved profiles', async () => {
    const pc = new ProfileClient(new Dispatcher())
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue(MOCK_ALIASES)
    vi.spyOn(pc, 'listProfiles').mockResolvedValue([mockProfile('other-name', '10.0.0.1')])
    const createSpy = vi.spyOn(pc, 'createProfile').mockResolvedValue(mockProfile('', ''))

    const result = await pc.importSSHConfig()
    expect(result.profilesImported).toBe(2)
    expect(result.skipped).toBe(1)
    expect(createSpy).toHaveBeenCalledTimes(2)
  })

  it('deduplicates aliases within the same batch by name', async () => {
    const pc = new ProfileClient(new Dispatcher())
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue({
      aliases: [
        { alias: 'dup-name', hostName: '10.0.0.1' },
        { alias: 'dup-name', hostName: '10.0.0.2' },
        { alias: 'unique', hostName: '10.0.0.3' },
      ],
      unavailable: null,
    })
    vi.spyOn(pc, 'listProfiles').mockResolvedValue([])
    const createSpy = vi.spyOn(pc, 'createProfile').mockResolvedValue(mockProfile('', ''))

    const result = await pc.importSSHConfig()
    expect(result.profilesImported).toBe(2)
    expect(createSpy).toHaveBeenCalledTimes(2)
    expect(result.skipped).toBe(1)
  })

  it('deduplicates aliases within the same batch by host', async () => {
    const pc = new ProfileClient(new Dispatcher())
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue({
      aliases: [
        { alias: 'first', hostName: '10.0.0.1' },
        { alias: 'second', hostName: '10.0.0.1' },
        { alias: 'third', hostName: '10.0.0.2' },
      ],
      unavailable: null,
    })
    vi.spyOn(pc, 'listProfiles').mockResolvedValue([])
    const createSpy = vi.spyOn(pc, 'createProfile').mockResolvedValue(mockProfile('', ''))

    const result = await pc.importSSHConfig()
    expect(result.profilesImported).toBe(2)
    expect(result.skipped).toBe(1)
    expect(createSpy).toHaveBeenCalledTimes(2)
  })

  it('maps alias fields into the created profile', async () => {
    const pc = new ProfileClient(new Dispatcher())
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue(MOCK_ALIASES)
    vi.spyOn(pc, 'listProfiles').mockResolvedValue([])
    const created: SSHProfile[] = []
    vi.spyOn(pc, 'createProfile').mockImplementation((p: SSHProfile) => {
      created.push(p)
      return Promise.resolve(p)
    })

    await pc.importSSHConfig()
    expect(created).toHaveLength(3)
    expect(created[0]).toMatchObject({
      name: 'prod-db',
      options: { host: '10.0.0.1', user: 'deploy', port: 2222 },
    })
    expect(created[1]).toMatchObject({
      name: 'dev-box',
      options: { host: 'dev.local', user: 'dev' },
    })
    expect(created[2]).toMatchObject({ name: 'staging', options: { host: 'staging.example.com' } })
  })

  it('throws when aliases are unavailable', async () => {
    const pc = new ProfileClient(new Dispatcher())
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue({
      aliases: [],
      unavailable: { reason: 'no-ssh-binary', detail: 'ssh not found' },
    })
    vi.spyOn(pc, 'listProfiles').mockResolvedValue([])

    await expect(pc.importSSHConfig()).rejects.toThrow('ssh not found')
  })

  it('propagates non-collision createProfile errors', async () => {
    const pc = new ProfileClient(new Dispatcher())
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue(MOCK_ALIASES)
    vi.spyOn(pc, 'listProfiles').mockResolvedValue([])
    vi.spyOn(pc, 'createProfile').mockRejectedValue(new Error('network error'))

    await expect(pc.importSSHConfig()).rejects.toThrow('network error')
  })
})

describe('adoptAliasProfile', () => {
  it('stores the alias as the host, not the resolved hostName', () => {
    const p = adoptAliasProfile('db-prod', 'deploy', 2222)
    expect(p.options.host).toBe('db-prod')
    expect(p.name).toBe('db-prod')
    expect(p.type).toBe('ssh')
  })

  it('sets user and port when provided', () => {
    const p = adoptAliasProfile('web', 'ops', 443)
    expect(p.options.user).toBe('ops')
    expect(p.options.port).toBe(443)
  })

  it('omits user when not provided', () => {
    const p = adoptAliasProfile('bare', undefined, undefined)
    expect(p.options.host).toBe('bare')
    expect(p.options).not.toHaveProperty('user')
    expect(p.options).not.toHaveProperty('port')
  })

  it('omits port when not provided', () => {
    const p = adoptAliasProfile('host-only', 'bob', undefined)
    expect(p.options.user).toBe('bob')
    expect(p.options).not.toHaveProperty('port')
  })

  it('omits both user and port when neither is configured', () => {
    const p = adoptAliasProfile('minimal', undefined, undefined)
    expect(p.options.host).toBe('minimal')
    expect(p.options).not.toHaveProperty('user')
    expect(p.options).not.toHaveProperty('port')
  })

  it('mints no id — the backend does that and returns the record', () => {
    // nocx-uxs5.10 removed renderer-side id minting. This asserts the absence
    // rather than the presence, because a dead newProfileID sitting in this
    // file is what invited two separate workers to call it again.
    const p = adoptAliasProfile('my-server', 'admin', 22)
    expect(p.id).toBe('')
    expect(p.type).toBe('ssh')
  })
})
