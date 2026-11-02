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
import type { UpdateCheckOutcome } from './update-check'
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
  build: '202608200941',
  date: '2026-08-20T09:41:00Z',
  go: 'go1.25.0',
  wails: 'v3.0.0-beta.9',
  platform: 'darwin/arm64',
  development: false,
}

const DEV: AppAbout = {
  version: 'dev',
  commit: 'none',
  build: 'unknown',
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
    // A surface's failed state is the kit StatusCard (nocx-8sudy 3/4) — a
    // state, not a notification, and never a private <p>.
    const card = document.querySelector('.ui-status-card')
    expect(card).not.toBeNull()
    expect(card?.getAttribute('data-tone')).toBe('danger')
    expect(screen.getByText(/could not read this build's details/i)).toBeTruthy()
  })

  it('says why the last update check failed, where a person goes to look (nocx-1ebrs.36)', async () => {
    // A soft degrade must be visible in the product: 0.6.0's failed checks
    // vanished in a silent catch, and the About page is where somebody who
    // wonders "why has it not offered me 0.6.1?" goes to read.
    render(() => (
      <AboutSection
        load={load(RELEASE)}
        clipboard={fakeClipboard()}
        updateCheckError={() => 'fetch manifest: unexpected status 500'}
      />
    ))

    await waitFor(() =>
      expect(
        screen.getByText('update check failed: fetch manifest: unexpected status 500'),
      ).toBeTruthy(),
    )
  })

  it('makes no claim about the update check when it has not failed', async () => {
    render(() => (
      <AboutSection
        load={load(RELEASE)}
        clipboard={fakeClipboard()}
        updateCheckError={() => null}
      />
    ))

    await waitFor(() => expect(screen.getByText(RELEASE.version)).toBeTruthy())
    expect(screen.queryByText(/update check failed/i)).toBeNull()
  })
})

// ── Check for updates: a person can ask now (nocx-1ebrs.55) ───────────────
//
// The bead's sentence: activate "Check for updates", it runs the existing
// createUpdateCheck().run owner, and the surface shows checking, then one of
// three answers. These assert through the seam a person actually reaches —
// the button, its disabled moment, and the line that lands beside it — with
// the check itself stubbed at the controller boundary, never re-implemented
// here.
describe('AboutSection Check for updates (nocx-1ebrs.55)', () => {
  it('offers the action where a check seam exists, and not where none does', async () => {
    render(() => (
      <AboutSection
        load={load(RELEASE)}
        clipboard={fakeClipboard()}
        onCheckForUpdates={() => Promise.resolve(null)}
      />
    ))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /check for updates/i })).toBeTruthy(),
    )

    cleanup()
    render(() => <AboutSection load={load(RELEASE)} clipboard={fakeClipboard()} />)
    await waitFor(() => expect(screen.getByText(RELEASE.version)).toBeTruthy())
    // No seam, no button: a control that cannot run is a dead control, and
    // the bead that owns this page already forbids those.
    expect(screen.queryByRole('button', { name: /check for updates/i })).toBeNull()
  })

  it('activates the check through the seam a person presses', async () => {
    const onCheck = vi
      .fn<() => Promise<UpdateCheckOutcome | null>>()
      .mockResolvedValue({ kind: 'up-to-date', channel: 'stable' })
    render(() => (
      <AboutSection load={load(RELEASE)} clipboard={fakeClipboard()} onCheckForUpdates={onCheck} />
    ))
    await waitFor(() => expect(screen.getByText(RELEASE.version)).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: /check for updates/i }))

    await waitFor(() => expect(onCheck).toHaveBeenCalledTimes(1))
  })

  it('says checking while the check runs, and reports up to date with the build and the channel', async () => {
    let answer: (() => void) | undefined
    const onCheck = () =>
      new Promise<{ kind: 'up-to-date'; channel: string }>((resolve) => {
        answer = () => resolve({ kind: 'up-to-date', channel: 'nightly' })
      })
    render(() => (
      <AboutSection load={load(RELEASE)} clipboard={fakeClipboard()} onCheckForUpdates={onCheck} />
    ))
    await waitFor(() => expect(screen.getByText(RELEASE.version)).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: /check for updates/i }))
    // Checking is a state a person can see, not a frozen page: the button
    // says so and holds.
    await waitFor(() => expect(screen.getByText(/checking…/i)).toBeTruthy())
    expect(screen.getByRole('button', { name: /checking…/i })).toBeTruthy()

    answer!()
    await waitFor(() =>
      expect(
        screen.getByText(`You're up to date (${RELEASE.version}, channel nightly)`),
      ).toBeTruthy(),
    )
    // The checking state is gone once there is an answer.
    expect(screen.queryByText(/checking…/i)).toBeNull()
  })

  it('shows the failure with its cause where the check itself already owns it', async () => {
    // A failed check resolves null and the shared error accessor carries the
    // cause — the card below is the surface a person reads, and it updates
    // live because it reads the accessor rather than a snapshot.
    const onCheck = vi.fn<() => Promise<UpdateCheckOutcome | null>>().mockResolvedValue(null)
    render(() => (
      <AboutSection
        load={load(RELEASE)}
        clipboard={fakeClipboard()}
        onCheckForUpdates={onCheck}
        updateCheckError={() => 'fetch manifest: unexpected status 500'}
      />
    ))
    await waitFor(() => expect(screen.getByText(RELEASE.version)).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: /check for updates/i }))
    await waitFor(() => expect(onCheck).toHaveBeenCalledTimes(1))

    expect(
      screen.getByText('update check failed: fetch manifest: unexpected status 500'),
    ).toBeTruthy()
    // And no up-to-date line: a failure is not quietly also a success.
    expect(screen.queryByText(/up to date/i)).toBeNull()
  })

  it('leaves the announcement of an available build to the toast path', async () => {
    // The available case is reported by the update toast the check raises
    // (nocx-1ebrs.54); this page does not keep a second wording of it. What
    // the page must not do is claim up-to-date.
    const onCheck = vi.fn<() => Promise<UpdateCheckOutcome | null>>().mockResolvedValue({
      kind: 'available',
      version: '0.7.0',
      notesUrl: 'https://example.com/notes',
    })
    render(() => (
      <AboutSection load={load(RELEASE)} clipboard={fakeClipboard()} onCheckForUpdates={onCheck} />
    ))
    await waitFor(() => expect(screen.getByText(RELEASE.version)).toBeTruthy())

    fireEvent.click(screen.getByRole('button', { name: /check for updates/i }))
    await waitFor(() => expect(onCheck).toHaveBeenCalledTimes(1))

    expect(screen.queryByText(/up to date/i)).toBeNull()
  })
})
