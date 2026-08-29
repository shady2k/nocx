import { describe, expect, it } from 'vitest'
import { createLivePolicy } from './live'
import type { LinkOpener } from './open'
import type { LinkTarget } from './detect'
import type { ArmedTracker } from './armed'
import type { ActiveOrigin } from '../pane-content'

const origin: Omit<ActiveOrigin, 'paneId'> = {
  sessionId: 's1',
  kind: 'local',
  cwd: '/repo',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

function armedAt(value: boolean): ArmedTracker {
  return { armed: () => value, subscribe: () => () => {}, dispose: () => {} }
}

function setup(armed = true) {
  const opened: LinkTarget[] = []
  const said: string[] = []
  const opener: LinkOpener = {
    open: (t) => {
      opened.push(t)
      return Promise.resolve()
    },
  }
  const policy = createLivePolicy({
    opener,
    origin: () => origin,
    armed: armedAt(armed),
    notify: (m) => said.push(m),
  })
  return { policy, opened, said }
}

describe('createLivePolicy — ranges', () => {
  it('offers the grammar’s spans while armed', () => {
    const { policy } = setup()
    expect(policy.ranges('see docs/x.md:2 now')).toEqual([{ from: 4, to: 15 }])
  })

  it('offers nothing while the modifier is up', () => {
    // The engine underlines whatever this returns, so an unarmed link would
    // be an underline under a click that does nothing.
    const { policy } = setup(false)
    expect(policy.ranges('see docs/x.md:2 now')).toEqual([])
  })

  it('offers nothing for a row with no links', () => {
    const { policy } = setup()
    expect(policy.ranges('total 12/20')).toEqual([])
  })
})

describe('createLivePolicy — activate', () => {
  it('opens the span at the given offsets', () => {
    const { policy, opened } = setup()
    policy.activate('see docs/x.md:2 now', 4, 15)
    expect(opened).toEqual([{ kind: 'path', path: 'docs/x.md', line: 2 }])
  })

  it('does nothing for offsets that are no longer a link', () => {
    // The row can be repainted between hover and click.
    const { policy, opened } = setup()
    policy.activate('see docs/x.md:2 now', 0, 3)
    expect(opened).toEqual([])
  })
})

describe('createLivePolicy — OSC 8', () => {
  it('follows a hyperlink a program declared', () => {
    const { policy, opened } = setup()
    policy.activateHyperlink('https://example.com/x')
    expect(opened).toEqual([{ kind: 'url', url: 'https://example.com/x' }])
  })

  it('refuses a scheme nocx does not open, and says so', () => {
    const { policy, opened, said } = setup()
    policy.activateHyperlink('file:///etc/passwd')
    expect(opened).toEqual([])
    expect(said).toHaveLength(1)
    expect(said[0]).toContain('file:///etc/passwd')
  })

  it('refuses a javascript: url', () => {
    const { policy, opened } = setup()
    policy.activateHyperlink('javascript:alert(1)')
    expect(opened).toEqual([])
  })

  it('follows a declared hyperlink even when the modifier is up', () => {
    // The engine gates OSC 8 activation itself; a program's declared link is
    // not something the grammar offered, so `ranges` has no say over it.
    const { policy, opened } = setup(false)
    policy.activateHyperlink('https://example.com/x')
    expect(opened).toHaveLength(1)
  })
})
