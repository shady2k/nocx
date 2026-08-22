// One owner for "what this machine is called". The tab strip's second line
// and the operations row must print the SAME string for the same machine,
// because a person reading a row minutes later has only that string to go
// on — and two spellings of one machine agree everywhere anybody looks
// until the day one of them has no user.
import { describe, expect, it } from 'vitest'
import { machineName, remoteMachineName, THIS_MACHINE } from './machine-name'

describe('naming a remote machine', () => {
  it('is user@host when a user is known', () => {
    expect(remoteMachineName('pi', '192.168.0.93')).toBe('pi@192.168.0.93')
  })

  it('is the bare host when no user is known', () => {
    // Not "@host", and not a guessed user: the name is what is known.
    expect(remoteMachineName(null, 'srv-01')).toBe('srv-01')
    expect(remoteMachineName('', 'srv-01')).toBe('srv-01')
    expect(remoteMachineName(undefined, 'srv-01')).toBe('srv-01')
  })

  it('is empty when there is no host at all', () => {
    // The EMPTY answer is the reason this function is separate from
    // machineName: the tab strip's second line falls back to the working
    // directory, and it can only do that if it can detect the local case.
    expect(remoteMachineName('me', null)).toBe('')
    expect(remoteMachineName(null, '')).toBe('')
  })
})

describe('naming a machine when a machine must be named', () => {
  it('says the same thing as remoteMachineName wherever there is a host', () => {
    // One derivation with a fallback bolted on, never a second derivation:
    // if these two could disagree there would be two names for one machine.
    for (const [user, host] of [
      ['pi', '192.168.0.93'],
      [null, 'srv-01'],
      ['deploy', 'web-prod.example.com'],
    ] as const) {
      expect(machineName(user, host)).toBe(remoteMachineName(user, host))
    }
  })

  it('names the local machine rather than leaving a blank', () => {
    // A row in the operations list is read out of the context of the tab
    // that started the work, so "the one with no host" is not something a
    // person can be left to infer.
    expect(machineName(null, null)).toBe('This machine')
    expect(machineName('me', null)).toBe(THIS_MACHINE)
  })
})
