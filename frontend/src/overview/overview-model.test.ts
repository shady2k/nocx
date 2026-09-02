import { describe, expect, it } from 'vitest'
import {
  ageLabel,
  cardLocation,
  cardProcess,
  cardTitle,
  overviewGroups,
  paneState,
  stateText,
  stateTone,
  workspaceAttention,
} from './overview-model'
import type { OverviewPaneFacts, OverviewSnapshot } from './overview-port'

function facts(over: Partial<OverviewPaneFacts> = {}): OverviewPaneFacts {
  return {
    paneId: 'p1',
    title: null,
    host: null,
    cwd: { state: 'unobserved' },
    process: { observed: false },
    branch: null,
    agentStatus: null,
    runningCommand: null,
    failed: false,
    since: null,
    lastLine: null,
    fullScreen: false,
    lastBlock: null,
    excerpt: [],
    ...over,
  }
}

const knownCwd = (cwd: string): OverviewPaneFacts['cwd'] => ({ state: 'known', cwd })

describe('what state a pane is in', () => {
  it("calls Claude's ✳ WAITING ON YOU, not idle", () => {
    // agent-status.ts: 'idle' is the ✳ marker, which means the agent has
    // stopped and is waiting for a human. Reading it as "nothing happening"
    // is the one inversion that would make this whole surface useless.
    expect(paneState(facts({ agentStatus: 'idle' }))).toBe('waiting')
    expect(stateText(facts({ agentStatus: 'idle' }), 0)).toBe('Waiting on you')
  })

  it('calls a spinner frame running', () => {
    expect(paneState(facts({ agentStatus: 'working' }))).toBe('running')
  })

  it('calls a foreground command running even with no agent signal', () => {
    expect(paneState(facts({ runningCommand: 'go test ./...' }))).toBe('running')
  })

  it('calls a shell at a prompt idle', () => {
    expect(paneState(facts({ title: '~/repos/nocx' }))).toBe('idle')
  })

  it('lets a failure beat every other signal', () => {
    expect(paneState(facts({ failed: true, agentStatus: 'working' }))).toBe('failed')
  })

  it('ignores a running command that is only whitespace', () => {
    expect(paneState(facts({ runningCommand: '   ' }))).toBe('idle')
  })

  it('gives each state a tone the dot and the sentence agree on', () => {
    expect(stateTone('failed')).toBe('error')
    expect(stateTone('waiting')).toBe('warning')
    expect(stateTone('running')).toBe('ok')
    expect(stateTone('idle')).toBe('neutral')
  })
})

describe('how long it has been in that state', () => {
  const now = 1_000_000_000

  it('is absent when nothing told us when the state began', () => {
    expect(ageLabel(null, now)).toBeNull()
    expect(stateText(facts({ agentStatus: 'idle' }), now)).toBe('Waiting on you')
  })

  it('counts seconds, then minutes, then hours, then days', () => {
    expect(ageLabel(now - 12_000, now)).toBe('12s')
    expect(ageLabel(now - 5 * 60_000, now)).toBe('5m')
    expect(ageLabel(now - 3 * 3_600_000, now)).toBe('3h')
    expect(ageLabel(now - 2 * 86_400_000, now)).toBe('2d')
  })

  it('reads as a duration for a live state and as an elapsed time for a dead one', () => {
    expect(stateText(facts({ agentStatus: 'idle', since: now - 300_000 }), now)).toBe(
      'Waiting on you for 5m',
    )
    expect(stateText(facts({ failed: true, since: now - 120_000 }), now)).toBe('Failed 2m ago')
  })

  it('does not run backwards when the clock disagrees with the timestamp', () => {
    expect(ageLabel(now + 5_000, now)).toBe('0s')
  })
})

describe('what a card says it is', () => {
  it('prefers the title the pane composed', () => {
    expect(
      cardTitle(facts({ title: 'claude', runningCommand: 'claude', cwd: knownCwd('~/x') })),
    ).toBe('claude')
  })

  it('falls back to the running command, then the cwd, then the host', () => {
    expect(cardTitle(facts({ runningCommand: 'go test ./...', cwd: knownCwd('~/x') }))).toBe(
      'go test ./...',
    )
    expect(cardTitle(facts({ cwd: knownCwd('~/repos/nocx') }))).toBe('~/repos/nocx')
    expect(cardTitle(facts({ host: 'deploy@srv-01' }))).toBe('deploy@srv-01')
  })

  it('still names a pane that has told us nothing at all', () => {
    // A pane one round trip old has no title, no cwd and no host. The card
    // must still be a card: a blank one is indistinguishable from a bug.
    expect(cardTitle(facts())).toBe('Untitled pane')
  })
})

describe('where a card says it is', () => {
  it('names host, cwd and branch when it knows them', () => {
    expect(
      cardLocation(
        facts({ title: 'claude', host: 'deploy@srv-01', cwd: knownCwd('~/app'), branch: 'main' }),
      ),
    ).toBe('deploy@srv-01 · ~/app · main')
  })

  it('renders the three cwd observation states differently', () => {
    const known = cardLocation(facts({ cwd: knownCwd('~/observed') }))
    const unobserved = cardLocation(facts({ cwd: { state: 'unobserved' } }))
    const unavailable = cardLocation(facts({ cwd: { state: 'unavailable' } }))

    expect(known).toBe('~/observed')
    expect(unobserved).toBe('Directory not observed')
    expect(unavailable).toBe('Directory unavailable')
    expect(new Set([known, unobserved, unavailable]).size).toBe(3)
  })

  it('does not show the launch directory when cwd observation is unavailable', () => {
    const pane = facts({ title: 'shell', cwd: { state: 'unavailable' } })
    const location = cardLocation(pane)
    expect(location).toBe('Directory unavailable')
    expect(location).not.toContain('~/launch')
  })

  it('does not use an unavailable cwd as the card title', () => {
    expect(cardTitle(facts({ cwd: { state: 'unavailable' } }))).toBe('Untitled pane')
  })

  it('does not repeat the title back as the location', () => {
    // The pane's own title is `programTitle || runningCommand || cwd`, so a
    // pane at a prompt is titled by its cwd — printing that cwd again under
    // it is one fact twice.
    expect(cardLocation(facts({ title: '~/repos/nocx', cwd: knownCwd('~/repos/nocx') }))).toBeNull()
  })
})

describe("what the card says about the session's own process", () => {
  const NOW = Date.parse('2026-09-02T12:00:00Z')
  const THREE_HOURS_EARLIER = NOW - 3 * 60 * 60 * 1000

  it('names the state, the age and the parent when the helper answered all three', () => {
    expect(
      cardProcess(
        facts({
          process: {
            observed: true,
            processState: 'sleeping',
            startTimeMs: THREE_HOURS_EARLIER,
            ppid: 4242,
          },
        }),
        NOW,
      ),
    ).toBe('Sleeping · started 3h ago · parent 4242')
  })

  it('tells a suspended shell apart from a live one, which nothing else in the product can', () => {
    const stopped = cardProcess(
      facts({
        process: { observed: true, processState: 'stopped', startTimeMs: null, ppid: null },
      }),
      NOW,
    )
    const sleeping = cardProcess(
      facts({
        process: { observed: true, processState: 'sleeping', startTimeMs: null, ppid: null },
      }),
      NOW,
    )
    expect(stopped).toContain('Stopped')
    expect(sleeping).toContain('Sleeping')
    expect(stopped).not.toBe(sleeping)
  })

  it('says which of the three it does not know, one at a time', () => {
    const observed = {
      observed: true,
      processState: 'running',
      startTimeMs: THREE_HOURS_EARLIER,
      ppid: 4242,
    } as const

    expect(cardProcess(facts({ process: { ...observed, processState: null } }), NOW)).toBe(
      'State unavailable · started 3h ago · parent 4242',
    )
    expect(cardProcess(facts({ process: { ...observed, startTimeMs: null } }), NOW)).toBe(
      'Running · start time unavailable · parent 4242',
    )
    expect(cardProcess(facts({ process: { ...observed, ppid: null } }), NOW)).toBe(
      'Running · started 3h ago · parent unavailable',
    )
  })

  it('says nothing at all when nobody could be asked, and something whenever one was', () => {
    // The distinction the two levels exist for. "No helper answered for this
    // pane" is silence; "the helper answered and knew none of the three" is a
    // sentence, because a reader must not read the second as the first and go
    // looking for the answer in the launch record.
    expect(cardProcess(facts({ process: { observed: false } }), NOW)).toBeNull()
    expect(
      cardProcess(
        facts({
          process: { observed: true, processState: null, startTimeMs: null, ppid: null },
        }),
        NOW,
      ),
    ).toBe('State unavailable · start time unavailable · parent unavailable')
  })
})

describe('the groups the overview draws', () => {
  function snapshot(over: Partial<OverviewSnapshot> = {}): OverviewSnapshot {
    return { workspaces: [], activePaneId: null, ...over }
  }

  it('is empty when there are no workspaces at all', () => {
    expect(overviewGroups(snapshot())).toEqual([])
  })

  it('keeps the default workspace nameless and puts it first', () => {
    // workspaces-ux §4.2: the default NEVER renders — no header, no name, no
    // colour. Its panes are simply ungrouped, and they come FIRST: that is
    // the order the horizontal strip already draws them in, and a person who
    // has never made a workspace has every pane they own in this group.
    const groups = overviewGroups(
      snapshot({
        workspaces: [
          { id: 'w-default', name: null, panes: [facts({ paneId: 'a' })] },
          { id: 'w1', name: 'refactor-auth', panes: [facts({ paneId: 'b' })] },
        ],
      }),
    )
    expect(groups.map((g) => g.id)).toEqual(['w-default', 'w1'])
    expect(groups[0].name).toBeNull()
    expect(groups[0].isDefault).toBe(true)
    expect(groups[1].isDefault).toBe(false)
  })

  it('carries a pane count and an attention state per workspace', () => {
    const groups = overviewGroups(
      snapshot({
        workspaces: [
          {
            id: 'w1',
            name: 'refactor-auth',
            panes: [facts({ paneId: 'a' }), facts({ paneId: 'b', agentStatus: 'idle' })],
          },
        ],
      }),
    )
    expect(groups[0].cards.length).toBe(2)
    expect(groups[0].attention).toBe('waiting')
  })

  it('survives a workspace the renderer drew no rows for', () => {
    const groups = overviewGroups(
      snapshot({ workspaces: [{ id: 'w1', name: 'empty', panes: [] }] }),
    )
    expect(groups.length).toBe(1)
    expect(groups[0].cards).toEqual([])
    expect(groups[0].attention).toBe('idle')
  })
})

describe("a workspace's attention state", () => {
  it('is the worst of its panes, and failure is worse than waiting', () => {
    expect(workspaceAttention(['idle', 'running'])).toBe('running')
    expect(workspaceAttention(['running', 'waiting'])).toBe('waiting')
    expect(workspaceAttention(['waiting', 'failed'])).toBe('failed')
  })

  it('is idle when there is nothing to be worst of', () => {
    expect(workspaceAttention([])).toBe('idle')
  })
})
