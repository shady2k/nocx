import { describe, expect, it } from 'vitest'
import {
  closingTabMessage,
  closingWorkspaceMessage,
  nameAtMost,
  type WorkspaceMember,
} from './live-work'

/** A member tab with nothing running on a local machine — the idle case. */
function idle(label: string): WorkspaceMember {
  return { label, command: null, host: null }
}

describe('the sentence a workspace close puts to a person (nocx-isoph.6, design D6)', () => {
  it('names the command that is running, not merely a count', () => {
    const message = closingWorkspaceMessage('refactor-auth', [
      idle('~/repos/nocx'),
      { label: 'srv-01', command: 'ansible-playbook deploy.yml', host: null },
    ])

    expect(message).toContain('ansible-playbook deploy.yml')
    // A count alone is what this exists to replace: it may say how many
    // tabs go, but never INSTEAD of saying what is in them.
    expect(message).toContain('2 tabs')
  })

  it('names the host a tab is talking to even when nothing is running on it', () => {
    const message = closingWorkspaceMessage('rollout', [
      { label: '~', command: null, host: 'deploy@prod-01' },
    ])

    expect(message).toContain('deploy@prod-01')
  })

  it('names the command and the machine it is running on together', () => {
    const message = closingWorkspaceMessage('rollout', [
      { label: '~', command: 'go test ./...', host: 'srv-01' },
    ])

    expect(message).toContain('“go test ./...” on srv-01')
  })

  it('says there is nothing running rather than naming an empty list', () => {
    const message = closingWorkspaceMessage('reading', [idle('~/notes'), idle('~/repos/nocx')])

    expect(message).toContain('2 tabs')
    expect(message).toMatch(/[Nn]othing is running/)
    // The honesty is the point: an all-idle workspace still asks, and the
    // question must not trail off into a list with no items in it.
    expect(message).not.toContain('Still running')
    expect(message).not.toContain('~/notes')
  })

  it('counts one tab in the singular', () => {
    expect(closingWorkspaceMessage('solo', [idle('~')])).toContain('1 tab.')
  })

  it('names the workspace, so a person closing one of several knows which', () => {
    expect(closingWorkspaceMessage('refactor-auth', [idle('~')])).toContain('refactor-auth')
  })

  // A tab holding no session at all — Settings, a file viewer — is still a
  // tab that closes, so it is counted; it is simply not something running.
  it('counts a tab that holds no session, and does not call it live', () => {
    const message = closingWorkspaceMessage('mixed', [
      idle('Settings'),
      { label: '~', command: 'make', host: null },
    ])

    expect(message).toContain('2 tabs')
    expect(message).toContain('“make”')
    expect(message).not.toContain('Settings')
  })
})

describe('closing one tab', () => {
  it('does not ask merely because an idle shell is remote', () => {
    expect(closingTabMessage({ label: '~', command: null, host: 'user@host' })).toBeNull()
    expect(closingTabMessage({ label: '~', command: '  ', host: 'user@host' })).toBeNull()
  })

  it('names a running remote command and its host before ending it', () => {
    const message = closingTabMessage({ label: '~', command: 'sleep 100000', host: 'user@host' })
    expect(message).toContain('sleep 100000')
    expect(message).toContain('user@host')
  })
})

describe('naming a list of live things before it becomes a wall', () => {
  it('names them all while there are few', () => {
    expect(nameAtMost(['a', 'b', 'c'])).toBe('a, b, c')
  })

  it('stops at five and counts the rest', () => {
    expect(nameAtMost(['a', 'b', 'c', 'd', 'e', 'f', 'g'])).toBe('a, b, c, d, e and 2 more')
  })

  it('reads as a sentence for one', () => {
    expect(nameAtMost(['a'])).toBe('a')
  })
})
