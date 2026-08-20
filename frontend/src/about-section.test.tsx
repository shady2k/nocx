// @vitest-environment jsdom
/**
 * AboutSection — what a person came to this page to do (nocx-8bbp).
 *
 * The page exists for one task: somebody is filing a bug, or checking whether
 * an update landed, and needs to know what build this is and be able to quote
 * it. So these assert through the seams that task actually goes through — the
 * values are on screen, the copy affordance exists and is reachable from the
 * state the page opens in, pressing it reaches the clipboard, and what lands
 * there is the whole block rather than a fragment.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@solidjs/testing-library'
import { AboutSection } from './about-section'
import type { AppAbout } from './generated/app.about'
import type { ClipboardAccess } from './clipboard'

const toasts: { message: string; level?: string }[] = []
vi.mock('./ui/toast', () => ({
  showToast: (t: { message: string; level?: string }) => {
    toasts.push(t)
  },
}))

const RELEASE: AppAbout = {
  version: '0.2.0',
  commit: '9f1c2b7d',
  date: '2026-08-20T09:41:00Z',
  go: 'go1.25.0',
  wails: 'v3.0.0-beta.9',
  platform: 'darwin/arm64',
  development: false,
}

const DEV: AppAbout = {
  version: 'dev',
  commit: 'none',
  date: 'unknown',
  go: 'go1.25.0',
  wails: 'v3.0.0-beta.9',
  platform: 'linux/amd64',
  development: true,
}

function fakeClipboard(): ClipboardAccess & { written: string[] } {
  const written: string[] = []
  return {
    written,
    readText: () => Promise.resolve(''),
    writeText: (text: string) => {
      written.push(text)
      return Promise.resolve()
    },
  }
}

const load = (about: AppAbout) => () => Promise.resolve(about)

beforeEach(() => {
  toasts.length = 0
})
afterEach(cleanup)

describe('AboutSection', () => {
  it('says what build this is, every field of it', async () => {
    render(() => <AboutSection load={load(RELEASE)} clipboard={fakeClipboard()} />)

    // Every value the bead names, because the page's whole job is to be read
    // out loud. A row that is present but blank would pass a "renders" test and
    // fail the person reading it.
    for (const value of Object.values(RELEASE)) {
      if (typeof value !== 'string') continue
      await waitFor(() => expect(screen.getByText(value)).toBeTruthy())
    }
  })

  it('shows the application icon', async () => {
    const { container } = render(() => (
      <AboutSection load={load(RELEASE)} clipboard={fakeClipboard()} />
    ))
    await waitFor(() => {
      const icon = container.querySelector('.ab-icon')
      expect(icon).not.toBeNull()
      expect(icon?.getAttribute('src')).toBeTruthy()
    })
  })

  // A DEV BUILD SAYS SO. "dev" is a placeholder, and a page that renders it in
  // the version's place presents it as a release number — which is exactly the
  // question this page is asked ("is this the build with the fix?").
  it('marks a development build rather than presenting dev as a release', async () => {
    render(() => <AboutSection load={load(DEV)} clipboard={fakeClipboard()} />)
    await waitFor(() => expect(screen.getByText(/development build/i)).toBeTruthy())
  })

  it('does not call a release a development build', async () => {
    render(() => <AboutSection load={load(RELEASE)} clipboard={fakeClipboard()} />)
    await waitFor(() => expect(screen.getByText('0.2.0')).toBeTruthy())
    expect(screen.queryByText(/development build/i)).toBeNull()
  })

  // ONE ACTION, THE WHOLE BLOCK. The reason anyone opens this page is to paste
  // it into a bug report, so a copy that carried the version alone would leave
  // them going back for the other five.
  it('copies the whole block in one action', async () => {
    const clipboard = fakeClipboard()
    render(() => <AboutSection load={load(RELEASE)} clipboard={clipboard} />)
    await waitFor(() => expect(screen.getByText('0.2.0')).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: /copy diagnostics/i }))

    await waitFor(() => expect(clipboard.written.length).toBe(1))
    const copied = clipboard.written[0]
    for (const value of Object.values(RELEASE)) {
      if (typeof value !== 'string') continue
      expect(copied).toContain(value)
    }
  })

  // A refusal is reported. The clipboard is a platform capability that can say
  // no (a non-secure context, a platform that refused the write), and a button
  // that silently does nothing is worse than one that is not there.
  it('says so when the clipboard refuses', async () => {
    const clipboard: ClipboardAccess = {
      readText: () => Promise.resolve(''),
      writeText: () => Promise.reject(new Error('no clipboard here')),
    }
    render(() => <AboutSection load={load(RELEASE)} clipboard={clipboard} />)
    await waitFor(() => expect(screen.getByText('0.2.0')).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: /copy diagnostics/i }))
    await waitFor(() => expect(toasts.some((t) => t.level === 'danger')).toBe(true))
  })

  // The backend can be unreachable, and the page then says that rather than
  // rendering six empty rows that look like a build with no identity.
  it('reports a build it could not read', async () => {
    render(() => (
      <AboutSection load={() => Promise.reject(new Error('down'))} clipboard={fakeClipboard()} />
    ))
    await waitFor(() => expect(screen.getByText(/could not read/i)).toBeTruthy())
  })
})
