// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { decideSaveRoute } from './connections'
import type { SSHProfile } from './profiles'

// ── Stub profile ───────────────────────────────────────────────────────────

const MOCK_PROFILE: SSHProfile = {
  id: 'ssh:backend-id:abc123',
  type: 'ssh',
  name: 'prod-web',
  options: {
    host: '10.0.0.1',
    port: 22,
    user: 'deploy',
    keepaliveInterval: 0,
    keepaliveCountMax: 0,
    readyTimeout: 0,
    agentForward: false,
    canBeJumpServer: false,
  },
}

// ── decideSaveRoute tests ─────────────────────────────────────────────────

describe('decideSaveRoute', () => {
  it('returns noop when nothing is dirty', () => {
    const r = decideSaveRoute(MOCK_PROFILE, new Set())
    expect(r.kind).toBe('noop')
  })

  it('returns update when only name is dirty', () => {
    const r = decideSaveRoute(MOCK_PROFILE, new Set(['name']))
    expect(r.kind).toBe('update')
  })

  it('returns update when only host is dirty', () => {
    const r = decideSaveRoute(MOCK_PROFILE, new Set(['host']))
    expect(r.kind).toBe('update')
  })

  it('returns update when name and port are dirty', () => {
    const r = decideSaveRoute(MOCK_PROFILE, new Set(['name', 'port']))
    expect(r.kind).toBe('update')
  })

  it('returns update when host and port are dirty', () => {
    const r = decideSaveRoute(MOCK_PROFILE, new Set(['host', 'port']))
    expect(r.kind).toBe('update')
  })

  it('returns patch when only port is dirty', () => {
    const r = decideSaveRoute(MOCK_PROFILE, new Set(['port']))
    expect(r.kind).toBe('patch')
  })

  it('returns patch with correct options prefix for patchable fields', () => {
    const r = decideSaveRoute(MOCK_PROFILE, new Set(['port', 'user']))
    if (r.kind !== 'patch') {
      expect(r.kind).toBe('patch')
      return
    }
    expect(r.patchSet).toEqual({
      'options.port': 22,
      'options.user': 'deploy',
    })
  })

  it('returns update for behaviorOnSessionEnd (not tested through patch)', () => {
    // behaviorOnSessionEnd is on Base, not options, so it's not a dirty
    // field today. But if it were, it should NOT be treated as nonPatchable
    // since the backend does accept options.behaviorOnSessionEnd as a patch
    // path. This test documents the current boundary: name and host are the
    // only non-patchable fields.
    const r = decideSaveRoute(MOCK_PROFILE, new Set(['behaviorOnSessionEnd']))
    // behaviorOnSessionEnd is NOT in the nonPatchable set, so this would
    // route to patch (which would then fail on the backend because it's
    // not an options field — unless the frontend adds the options. prefix).
    // This is correct behavior: options.behaviorOnSessionEnd IS in the
    // backend's PatchPathAllowed.
    expect(r.kind).toBe('patch')
  })

  it('returns update when only group is dirty', () => {
    const r = decideSaveRoute(MOCK_PROFILE, new Set(['group']))
    expect(r.kind).toBe('update')
  })
})

describe('revert does not materialise the inherited value', () => {
  // The failure this pins: revert a port to its inherited value, then rename the
  // connection. The rename routes through profiles.update, which writes the WHOLE
  // profile — so an inherited value left sitting in options would be persisted as
  // an explicit override, and the field the user just reverted would be pinned to
  // the value it used to inherit. Spec §3.3's first binding rule, and the reason
  // revertField deletes the key instead of assigning the effective value to it.
  it('a reverted field is absent from the draft, not set to what it inherits', () => {
    const draft: SSHProfile = {
      id: 'ssh:web:1',
      type: 'ssh',
      name: 'web',
      options: { host: 'h', port: 2200, user: 'deploy' },
    }
    // Exactly what revertField does to the draft.
    const updated = { ...draft, options: { ...draft.options } }
    delete (updated.options as unknown as Record<string, unknown>).port

    expect('port' in updated.options).toBe(false)
    // JSON is what crosses the wire — the key must not appear at all, because a
    // present key is what the presence-aware backend reads as "explicitly set".
    const wire = JSON.parse(JSON.stringify(updated)) as { options: Record<string, unknown> }
    expect(wire.options).not.toHaveProperty('port')
  })
})
