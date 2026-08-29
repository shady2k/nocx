// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import {
  createRendererMock,
  resetSessionCounter,
  mountPaneManager,
} from './test-support/panes-fixtures'

// A window is told when another one takes a pane's session (nocx-oevq4, the
// nocx-server design D8).
//
// It lives in its own file for the toast mock: the toast host is mounted by
// App.tsx, which these tests do not mount, so the outcome is asserted where it
// is raised. That is the point of the test — a displacement the product only
// writes to a log leaves a terminal that looks live and swallows every
// keystroke, which is the surface AGENTS.md says must not exist.
vi.mock('./renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

const showToastMock = vi.fn()
vi.mock('./ui/toast', () => ({
  showToast: (...args: unknown[]) => {
    showToastMock(...args)
  },
}))

describe('a session taken by another window', () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
  })

  it('tells the person, rather than leaving a terminal that answers nothing', async () => {
    const { client } = await mountPaneManager()

    client._fireDisplaced()

    expect(showToastMock).toHaveBeenCalledTimes(1)
    const [options] = showToastMock.mock.calls[0] as [{ level: string; message: string }]
    expect(options.level).toBe('warning')
    expect(options.message).toContain('another window')
  })

  // The manager subscribes ONCE, when it is built — not per pane and not per
  // open. A window that subscribed on opening a pane would say nothing about
  // the panes it reclaimed, which are exactly the ones a second window is
  // most likely to take.
  it('is subscribed from the moment the window exists', async () => {
    const { client } = await mountPaneManager()
    expect(client.onSessionDisplaced).toHaveBeenCalledTimes(1)
  })
})
