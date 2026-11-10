// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { createUpdateController } from './update-controller'
import { downloadProgressLabel } from './update-details'
import { ToastHost } from './ui/toast'
import { mountSidebar } from './sidebar'

const { logWarn } = vi.hoisted(() => ({ logWarn: vi.fn() }))
vi.mock('./log', () => ({ log: { warn: logWarn } }))

const NOTES = 'https://example.com/releases/2.0.0'
afterEach(() => {
  cleanup()
  document.body.replaceChildren()
})

function mountIndicator(controller: ReturnType<typeof createUpdateController>) {
  const bar = document.createElement('div')
  const panel = document.createElement('div')
  document.body.append(bar, panel)
  const handle = mountSidebar(
    bar,
    panel,
    [],
    [{ id: 'settings', title: () => 'Settings', icon: () => <svg />, onActivate: vi.fn() }],
    null,
    undefined,
    undefined,
    undefined,
    undefined,
    {
      title: () => 'Update available',
      icon: () => <svg />,
      level: () => 'neutral',
      available: () => controller.state().kind !== 'hidden',
      onActivate: controller.showOffer,
    },
  )
  return { bar, panel, handle }
}

describe('shared update offer', () => {
  it.each(['throw', 'string'])('keeps held update retryable after a %s refusal', async (shape) => {
    const raw = 'stop failed /private/state Errno 5'
    const finish = vi
      .fn<() => Promise<void>>()
      .mockImplementationOnce(() => {
        if (shape === 'throw') throw new Error(raw)
        // Deliberately exercise a non-Error rejection from a native boundary.
        // eslint-disable-next-line @typescript-eslint/prefer-promise-reject-errors
        return Promise.reject(raw)
      })
      .mockResolvedValue(undefined)
    const controller = createUpdateController({ finish })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showHeldByCoordinator('2.0.0')
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    fireEvent.click(screen.getByRole('button', { name: 'Close nocx' }))
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull(),
    )
    expect(screen.getByRole('status').textContent).not.toMatch(/\/|Errno/)
    expect(logWarn).toHaveBeenCalledWith('update action failed', { error: raw })
    controller.showAvailable('2.0.1', NOTES)
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.getByRole('status').textContent).toContain('2.0.0 installed')
    fireEvent.click(screen.getByRole('button', { name: 'Close nocx' }))
    await vi.waitFor(() => expect(finish).toHaveBeenCalledTimes(2))
  })

  it.each(['held-first', 'check-first'])(
    'reads held startup through the startup seam: %s',
    async (order) => {
      const controller = createUpdateController({ finish: vi.fn() })
      render(() => <ToastHost />)
      mountIndicator(controller)
      if (order === 'check-first') controller.showAvailable('2.0.1', NOTES)
      await controller.restore({
        getState: () => Promise.resolve({ HeldByOlderCoordinator: true, Version: '2.0.0' }),
      })
      if (order === 'held-first') controller.showAvailable('2.0.1', NOTES)
      fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
      expect(screen.getByRole('status').textContent).toContain('2.0.0 installed')
      expect(screen.getByRole('status').textContent).toContain('closes open sessions')
      expect(screen.getAllByRole('button', { name: 'Close nocx' })).toHaveLength(1)
      expect(screen.queryByRole('button', { name: /^Install/ })).toBeNull()
    },
  )

  it('leaves ordinary pending startup quiet and logs a failed state read', async () => {
    const controller = createUpdateController()
    render(() => <ToastHost />)
    mountIndicator(controller)
    await controller.restore({
      getState: () => Promise.resolve({ HeldByOlderCoordinator: false, Version: '2.0.0' }),
    })
    expect(screen.queryByRole('button', { name: 'Update available' })).toBeNull()
    const error = new Error('state read failed /private/state')
    await controller.restore({ getState: () => Promise.reject(error) })
    expect(logWarn).toHaveBeenCalledWith('reading update state failed', { error: error.message })
    expect(screen.queryByRole('button', { name: 'Update available' })).toBeNull()
  })

  it('keeps a held coordinator visible across checks and finishes only once, with the session warning', async () => {
    let resolve!: () => void
    const finish = vi.fn(
      () =>
        new Promise<void>((r) => {
          resolve = r
        }),
    )
    const apply = vi.fn()
    const restart = vi.fn()
    const controller = createUpdateController({ apply, restart, finish })
    render(() => <ToastHost />)
    const first = mountIndicator(controller)
    controller.showHeldByCoordinator('2.0.0')
    controller.showAvailable('2.0.1', NOTES)
    first.handle.destroy()
    first.bar.remove()
    first.panel.remove()
    mountIndicator(controller)
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.getByRole('status').textContent).toContain('2.0.0 installed')
    expect(screen.getByRole('status').textContent).toContain('closes open sessions')
    expect(screen.queryByRole('button', { name: /^Install/ })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Close nocx' }))
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.queryByRole('button', { name: 'Close nocx' })).toBeNull()
    await vi.waitFor(() => expect(finish).toHaveBeenCalledTimes(1))
    expect(apply).not.toHaveBeenCalled()
    expect(restart).not.toHaveBeenCalled()
    resolve()
  })

  it('explains a refused held finish without losing the installed version or retry', async () => {
    const error = new Error('stop failed /private/state Errno 5')
    const finish = vi.fn().mockRejectedValueOnce(error).mockResolvedValue(undefined)
    const controller = createUpdateController({ finish })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('2.0.1', NOTES)
    controller.showHeldByCoordinator('2.0.0')
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    fireEvent.click(screen.getByRole('button', { name: 'Close nocx' }))
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull(),
    )
    expect(screen.getByRole('status').textContent).not.toMatch(/\/|Errno/)
    expect(logWarn).toHaveBeenCalledWith('update action failed', { error: error.message })
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.getByRole('status').textContent).toContain('2.0.0 installed')
    fireEvent.click(screen.getByRole('button', { name: 'Close nocx' }))
    await vi.waitFor(() => expect(finish).toHaveBeenCalledTimes(2))
  })

  it.each([
    [
      'announcement',
      'update apply: preflight refused: cannot install the update; allow writes to Applications and try again: /private/tmp/nocx Errno 13',
      'cannot install the update; allow writes to Applications and try again',
    ],
    [
      'announcement',
      'update apply: preflight refused: cannot find your home folder — check your account settings and try again: /private/tmp/nocx Errno 13',
      'cannot find your home folder — check your account settings and try again',
    ],
    [
      'announcement',
      'update apply: preflight refused: the Applications folder is not writable — allow writes to Applications and try again: /private/tmp/nocx Errno 13',
      'the Applications folder is not writable — allow writes to Applications and try again',
    ],
    [
      'announcement',
      'update apply: preflight refused: the AppImage directory is not writable — move the AppImage to a directory you can write to, launch it there, and try again: /private/tmp/nocx Errno 13',
      'the AppImage directory is not writable — move the AppImage to a directory you can write to, launch it there, and try again',
    ],
    [
      'announcement',
      'update apply: preflight refused: this is a development build; self-update is not available: /private/tmp/nocx Errno 13',
      'this is a development build; self-update is not available',
    ],
    [
      'announcement',
      'update apply: preflight refused: this is not a distributed AppImage build: /private/tmp/nocx Errno 13',
      'this is not a distributed AppImage build',
    ],
    [
      'announcement',
      'update apply: preflight refused: rollback: this was a first installation, with no previous target to restore: /private/tmp/nocx Errno 13',
      'rollback: this was a first installation, with no previous target to restore',
    ],
    [
      'announcement',
      'update apply: preflight refused: the update install target is unavailable: /private/tmp/nocx Errno 13',
      'the update install target is unavailable',
    ],
    [
      'announcement',
      'update apply: preflight refused: this window cannot quit the application: /private/tmp/nocx Errno 13',
      'this window cannot quit the application',
    ],
    [
      'announcement',
      'update apply: preflight refused: state directory is not configured: /private/tmp/nocx Errno 13',
      'state directory is not configured',
    ],
    [
      'announcement',
      'download /tmp/update Errno 5',
      'The update could not be downloaded. Check your connection and try again.',
    ],
    [
      'announcement',
      'verify signature /tmp/nocx',
      'The update could not be verified. Check for updates and download it again.',
    ],
    [
      'announcement',
      'extract /tmp/nocx Errno 28',
      'The update could not be unpacked. Check free space and try again.',
    ],
    [
      'announcement',
      'exchange /Applications/nocx Errno 13',
      'The update could not replace the app. Allow writes to the app’s folder and try again.',
    ],
    [
      'announcement',
      'not installed at /private/app',
      'This copy of nocx cannot be updated here. Install the latest release in a folder you can write to, then try again.',
    ],
    [
      'announcement',
      'unknown secret /private/log Errno 7',
      'The update could not finish. Try again, or install the latest release manually.',
    ],
    [
      'indicator',
      'update apply: preflight refused: cannot install the update; allow writes to Applications and try again: /private/tmp/nocx Errno 13',
      'cannot install the update; allow writes to Applications and try again',
    ],
    [
      'indicator',
      'update apply: preflight refused: cannot find your home folder — check your account settings and try again: /private/tmp/nocx Errno 13',
      'cannot find your home folder — check your account settings and try again',
    ],
    [
      'indicator',
      'update apply: preflight refused: the Applications folder is not writable — allow writes to Applications and try again: /private/tmp/nocx Errno 13',
      'the Applications folder is not writable — allow writes to Applications and try again',
    ],
    [
      'indicator',
      'update apply: preflight refused: the AppImage directory is not writable — move the AppImage to a directory you can write to, launch it there, and try again: /private/tmp/nocx Errno 13',
      'the AppImage directory is not writable — move the AppImage to a directory you can write to, launch it there, and try again',
    ],
    [
      'indicator',
      'update apply: preflight refused: this is a development build; self-update is not available: /private/tmp/nocx Errno 13',
      'this is a development build; self-update is not available',
    ],
    [
      'indicator',
      'update apply: preflight refused: this is not a distributed AppImage build: /private/tmp/nocx Errno 13',
      'this is not a distributed AppImage build',
    ],
    [
      'indicator',
      'update apply: preflight refused: rollback: this was a first installation, with no previous target to restore: /private/tmp/nocx Errno 13',
      'rollback: this was a first installation, with no previous target to restore',
    ],
    [
      'indicator',
      'update apply: preflight refused: the update install target is unavailable: /private/tmp/nocx Errno 13',
      'the update install target is unavailable',
    ],
    [
      'indicator',
      'update apply: preflight refused: this window cannot quit the application: /private/tmp/nocx Errno 13',
      'this window cannot quit the application',
    ],
    [
      'indicator',
      'update apply: preflight refused: state directory is not configured: /private/tmp/nocx Errno 13',
      'state directory is not configured',
    ],
    [
      'indicator',
      'download /tmp/update Errno 5',
      'The update could not be downloaded. Check your connection and try again.',
    ],
    [
      'indicator',
      'verify signature /tmp/nocx',
      'The update could not be verified. Check for updates and download it again.',
    ],
    [
      'indicator',
      'extract /tmp/nocx Errno 28',
      'The update could not be unpacked. Check free space and try again.',
    ],
    [
      'indicator',
      'exchange /Applications/nocx Errno 13',
      'The update could not replace the app. Allow writes to the app’s folder and try again.',
    ],
    [
      'indicator',
      'not installed at /private/app',
      'This copy of nocx cannot be updated here. Install the latest release in a folder you can write to, then try again.',
    ],
    [
      'indicator',
      'unknown secret /private/log Errno 7',
      'The update could not finish. Try again, or install the latest release manually.',
    ],
  ])(
    'keeps %s install failure detail in the log and says safe words for %s',
    async (route, raw, expected) => {
      const error = new Error(raw)
      const apply = vi.fn().mockRejectedValueOnce(error).mockResolvedValue(undefined)
      const controller = createUpdateController({ apply })
      render(() => <ToastHost />)
      mountIndicator(controller)
      controller.showAvailable('2.0.0', NOTES)
      if (route === 'indicator')
        fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
      fireEvent.click(
        screen.getByRole('button', { name: route === 'indicator' ? /^Install$/ : 'Install now' }),
      )
      await vi.waitFor(() => expect(screen.getByRole('status').textContent).toContain(expected))
      expect(screen.getByRole('status').textContent).not.toMatch(/\/|Errno/)
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull()
      expect(logWarn).toHaveBeenCalledWith('update action failed', { error: raw })
      fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
      fireEvent.click(screen.getByRole('button', { name: /^Install$/ }))
      await vi.waitFor(() =>
        expect(screen.getByRole('status').textContent).toContain('2.0.0 installed'),
      )
      expect(apply).toHaveBeenCalledTimes(2)
    },
  )

  it.each(['announcement', 'indicator'])(
    'opens %s release notes through the synchronous opener and explains refusal',
    async (route) => {
      const open = vi.fn<(url: string) => Promise<void>>().mockResolvedValue(undefined)
      const controller = createUpdateController({ apply: vi.fn() })
      render(() => <ToastHost openUrl={open} />)
      mountIndicator(controller)
      controller.showAvailable('2.0.0', NOTES)
      if (route === 'indicator')
        fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
      const link = screen.getByRole('link', { name: 'Release notes' })
      fireEvent.click(link)
      expect(open).toHaveBeenCalledWith(NOTES)
      expect(screen.getByRole('status').textContent).toContain('2.0.0')
      open.mockRejectedValueOnce(new Error('browser refused /private/path Errno 13'))
      fireEvent.click(link)
      await vi.waitFor(() =>
        expect(screen.getByRole('status').textContent).toContain(
          'The browser could not be opened.',
        ),
      )
      expect(screen.getByRole('status').textContent).not.toContain('/private/path')
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull()
    },
  )

  it('announces once per version even after Later or dismiss, while the indicator stays findable', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('2.0.0', NOTES)
    expect(screen.getAllByRole('button', { name: 'Install now' })).toHaveLength(1)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'Later' }))
    controller.showAvailable('2.0.0', NOTES)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.getAllByRole('button', { name: /^Install$/ })).toHaveLength(1)
    controller.showAvailable('2.0.1', NOTES)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(1)
    expect(screen.getByRole('status').textContent).toContain('2.0.1')
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss notification' }))
    controller.showAvailable('2.0.1', NOTES)
    controller.showAvailable('2.0.0', NOTES)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(0)
  })

  it('the announcement uses the same single-flight apply and keeps replacement progress after the kit action dismisses', async () => {
    let resolve!: () => void
    const apply = vi.fn(
      () =>
        new Promise<void>((r) => {
          resolve = r
        }),
    )
    const restart = vi.fn()
    const controller = createUpdateController({ apply, restart })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('2.0.0', NOTES)
    fireEvent.click(screen.getByRole('button', { name: 'Install now' }))
    expect(screen.getByRole('status').textContent).toContain('Downloading nocx 2.0.0')
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    await controller.apply()
    controller.showAvailable('2.0.1', NOTES)
    expect(apply).toHaveBeenCalledTimes(1)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(1)
    resolve()
    await vi.waitFor(() =>
      expect(screen.getByRole('status').textContent).toContain('2.0.0 installed'),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Restart now' }))
    await vi.waitFor(() => expect(restart).toHaveBeenCalledTimes(1))
  })

  it('opens one offer beside Settings without taking tab space and applies once across sidebar rebuilds', async () => {
    let resolve!: () => void
    const apply = vi.fn(
      () =>
        new Promise<void>((r) => {
          resolve = r
        }),
    )
    const controller = createUpdateController({ apply })
    render(() => (
      <>
        <div id="tabbar" />
        <ToastHost />
      </>
    ))
    controller.showAvailable('2.0.0', NOTES)
    const first = mountIndicator(controller)
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    expect(screen.getByRole('status').textContent).toContain('2.0.0')
    expect(screen.getAllByRole('button', { name: /^Install$/ })).toHaveLength(1)
    expect(document.querySelector('#tabbar')?.textContent).toBe('')
    fireEvent.click(screen.getByRole('button', { name: /^Install$/ }))
    expect(apply).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('status').textContent).toContain('Downloading')
    first.handle.destroy()
    first.bar.remove()
    first.panel.remove()
    mountIndicator(controller)
    fireEvent.click(screen.getByRole('button', { name: 'Update available' }))
    controller.showAvailable('2.0.1', NOTES)
    await controller.apply()
    expect(apply).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('button', { name: /^Install$/ })).toBeNull()
    resolve()
    await vi.waitFor(() =>
      expect(screen.getByRole('status').textContent).toContain('2.0.0 installed'),
    )
  })

  it('says a failed startup-state read out loud, not only in the log (review, .66.1.1)', async () => {
    const controller = createUpdateController()
    render(() => <ToastHost />)
    mountIndicator(controller)

    const onFailure = vi.fn()
    await controller.restore({
      getState: () => Promise.reject(new Error('socket closed')),
      onFailure,
    })

    // The controller reports; the composition root decides what a person sees
    // (the desktop window says it in a warning toast, a browser stand has no
    // updater to warn about — review, nocx-1ebrs.66.1.1).
    expect(logWarn).toHaveBeenCalledWith('reading update state failed', expect.anything())
    expect(onFailure).toHaveBeenCalledWith('socket closed')
  })
})

// ── A nightly offer names the build it would install (nocx-vh2hr.1.5) ──────
//
// The owner's report: installed 0.8.1 (commit b905772), offered a nightly of
// the same 0.8.1 built from 60caa03d — and the notice said "nocx 0.8.1 is
// available" to someone already on 0.8.1, with release notes that do not
// exist for a nightly. On the nightly channel the notice names the offered
// build, and its link is the range the backend composed, or nothing.
describe('a nightly offer names the build (nocx-vh2hr.1.5)', () => {
  // What the backend composes when the installed and offered builds are both
  // stamped: the manifest's own host and repository, one commit range.
  const FROM = 'a'.repeat(40)
  const OFFERED = '60caa03d' + 'b'.repeat(32)
  const COMPARE = `https://github.com/shady2k/nocx/compare/${FROM}...${OFFERED}`
  const NIGHTLY = {
    channel: 'nightly',
    build: { commit: OFFERED, builtAt: '2026-10-30T07:50:35Z', number: '202610300750' },
  }

  it('names the offered build beside the version it shares with the installed one', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.8.1', COMPARE, NIGHTLY)

    expect(screen.getByRole('status').textContent).toContain('nocx 0.8.1, build 202610300750')
    expect(screen.getByRole('status').textContent).toContain('is available')
    expect(screen.getByRole('link', { name: 'What changed' }).getAttribute('href')).toBe(COMPARE)
  })

  it('names the build on a nightly that also moves the version', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.8.2', COMPARE, NIGHTLY)

    expect(screen.getByRole('status').textContent).toContain('nocx 0.8.2, build 202610300750')
    expect(screen.getByRole('link', { name: 'What changed' }).getAttribute('href')).toBe(COMPARE)
  })

  it('renders no link when the backend could not compose a range', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.8.1', '', NIGHTLY)

    expect(screen.getByRole('status').textContent).toContain('build 202610300750')
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('leaves the stable offer exactly as it was', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('2.0.1', NOTES, {
      channel: 'stable',
      build: { commit: OFFERED, builtAt: '2026-10-30T07:50:35Z', number: '202610300750' },
    })

    const message = screen.getByRole('status').textContent ?? ''
    expect(message).toContain('nocx 2.0.1 is available')
    expect(message).not.toContain('build')
    expect(screen.getByRole('link', { name: 'Release notes' }).getAttribute('href')).toBe(NOTES)
  })

  it('announces a second nightly of the same version as its own build', () => {
    const controller = createUpdateController({ apply: vi.fn() })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.8.1', COMPARE, NIGHTLY)
    fireEvent.click(screen.getByRole('button', { name: 'Later' }))

    const later = '70caa03d' + 'c'.repeat(32)
    controller.showAvailable('0.8.1', COMPARE, {
      channel: 'nightly',
      build: { commit: later, builtAt: '2026-10-31T07:50:35Z', number: '202610310750' },
    })

    expect(document.querySelectorAll('.ui-toast')).toHaveLength(1)
    expect(screen.getByRole('status').textContent).toContain('build 202610310750')
  })
})

// ── The notice says how far the download has got (nocx-vh2hr.1.15) ─────────
//
// The bead's other half: an update the person cannot see arriving. The bytes
// are the backend's (UpdateState.Downloading/Downloaded/Total); the notice
// reads them through the binding it already consults, shows percent and size
// while they move, and forgets them the moment the apply lands or fails.
describe('the notice shows the download (nocx-vh2hr.1.15)', () => {
  const progress = (downloaded: number, total: number) => ({
    Downloading: true,
    Downloaded: downloaded,
    Total: total,
  })

  it('draws a real progress bar with its figure while the body arrives, then says it is installed', async () => {
    let finishApply!: () => void
    const apply = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finishApply = resolve
        }),
    )
    const readings = [
      progress(0, 150_000_000),
      progress(63_000_000, 150_000_000),
      progress(99_000_000, 150_000_000),
    ]
    const getState = vi.fn(() => Promise.resolve(readings.shift() ?? { Downloading: false }))
    const controller = createUpdateController({ apply, getState })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.9.0', '')

    void controller.apply()
    await vi.waitFor(() => {
      expect(
        screen.getByRole('progressbar', { name: 'Update download' }).getAttribute('aria-valuenow'),
      ).toBe('42')
    })
    // THE FIGURE SITS BESIDE THE BAR, in the catalogue's words: percent and
    // both sizes, the two numbers update-details computes (nocx-vh2hr.1.25).
    expect(screen.getByRole('status').textContent).toContain('42% · 63 of 150 MB')
    expect(screen.getByRole('status').textContent).toContain('nocx 0.9.0')
    const bar = screen.getByRole('progressbar', { name: 'Update download' })
    const notice = bar.closest('.ui-toast')
    expect(notice).not.toBeNull()
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(1)

    // THE NOTICE IS REPAINTED, NOT RE-RAISED: the next step moves the same
    // bar inside the same toast — one animation, one announcement, one
    // element the person is reading (nocx-vh2hr.1.25).
    await vi.waitFor(() => {
      expect(
        screen.getByRole('progressbar', { name: 'Update download' }).getAttribute('aria-valuenow'),
      ).toBe('66')
    })
    expect(screen.getByRole('progressbar', { name: 'Update download' })).toBe(bar)
    expect(document.querySelectorAll('.ui-toast')).toHaveLength(1)

    finishApply()
    await vi.waitFor(() => {
      expect(screen.getByRole('status').textContent).toContain('restart to apply')
    })
    expect(screen.queryByRole('progressbar')).toBeNull()
    expect(screen.getByRole('status').textContent).not.toContain('Downloading')
  })

  it('shows no bar until the size is known — a determinate bar at zero reads as stalled', async () => {
    let finishApply!: () => void
    const apply = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finishApply = resolve
        }),
    )
    const getState = vi.fn(() => Promise.resolve({ Downloading: true, Downloaded: 0, Total: 0 }))
    const controller = createUpdateController({ apply, getState })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.9.0', '')
    void controller.apply()
    await vi.waitFor(() => {
      expect(screen.getByRole('status').textContent).toContain('Downloading nocx 0.9.0')
    })
    expect(screen.queryByRole('progressbar')).toBeNull()
    finishApply()
  })

  it('keeps the failure, not the progress, when the apply refuses', async () => {
    const apply = vi.fn(() => Promise.reject(new Error('download sha256 mismatch')))
    const getState = vi.fn(() => Promise.resolve(progress(15_000_000, 150_000_000)))
    const controller = createUpdateController({ apply, getState })
    render(() => <ToastHost />)
    mountIndicator(controller)
    controller.showAvailable('0.9.0', '')

    await controller.apply()
    await vi.waitFor(() => {
      expect(screen.getByRole('status').textContent).toContain('could not be downloaded')
    })
    expect(screen.getByRole('status').textContent).not.toContain('Downloading')
  })
})

// The label itself: floored percent, decimal megabytes, never 100% early.
describe('downloadProgressLabel (nocx-vh2hr.1.15)', () => {
  it('reads as a person would say it', () => {
    expect(downloadProgressLabel({ downloaded: 63_000_000, total: 150_000_000 })).toBe(
      'Downloading 42% · 63 of 150 MB',
    )
    expect(downloadProgressLabel({ downloaded: 149_999_999, total: 150_000_000 })).toBe(
      'Downloading 99% · 150 of 150 MB',
    )
    expect(downloadProgressLabel({ downloaded: 0, total: 0 })).toBe('Downloading…')
  })
})

// 'COPY DETAILS' ON A FAILED UPDATE (nocx-vh2hr.1.16, the owner's decision of
// 2026-11-02). The toast stays the safe sentence; the action beside it puts the
// cause on the clipboard, which is where somebody filing a bug can reach it.
// The path is the real one: a click on the toast's own button, through the kit.
describe('copy details on a failed update (nocx-vh2hr.1.16)', () => {
  const RAW =
    'update apply: download write: context deadline exceeded: /Users/ada/Library/Caches/nocx/update.zip'
  const OFFER = {
    channel: 'nightly',
    build: { commit: 'a'.repeat(40), builtAt: '2026-11-02T11:01:00Z', number: '202611021101' },
  }
  const ABOUT = { version: '0.8.0', build: '202610311830', platform: 'darwin/arm64' }

  function failed() {
    const controller = createUpdateController({
      apply: vi.fn().mockRejectedValue(new Error(RAW)),
      about: () => Promise.resolve(ABOUT),
      getState: () => Promise.resolve({ Pending: true, Downloaded: 0, Total: 0 }),
    })
    render(() => <ToastHost />)
    controller.showAvailable('0.8.1', NOTES, OFFER)
    fireEvent.click(screen.getByRole('button', { name: 'Install now' }))
    return controller
  }

  it('copies the error, its stage, the build and the platform, and the toast stays safe', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    const controller = createUpdateController({
      apply: vi.fn().mockRejectedValue(new Error(RAW)),
      about: () => Promise.resolve(ABOUT),
      getState: () => Promise.resolve({ Pending: true, Downloaded: 0, Total: 0 }),
      clipboard: { writeText },
    })
    render(() => <ToastHost />)
    controller.showAvailable('0.8.1', NOTES, OFFER)
    fireEvent.click(screen.getByRole('button', { name: 'Install now' }))
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull(),
    )

    // The toast itself is still the sentence a person can read at a glance.
    const shown = screen.getByRole('status').textContent ?? ''
    expect(shown).toContain('could not be downloaded')
    expect(shown).not.toContain('context deadline')
    expect(shown).not.toContain('/Users/ada')

    fireEvent.click(screen.getByRole('button', { name: 'Copy details' }))
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
    const text = writeText.mock.calls[0][0] as string
    expect(text).toContain('stage: apply')
    expect(text).toContain(
      'error: update apply: download write: context deadline exceeded: ~/Library/Caches/nocx/update.zip',
    )
    expect(text).toContain('version: 0.8.1')
    expect(text).toContain('build: 202611021101')
    expect(text).toContain('channel: nightly')
    expect(text).toContain('platform: darwin/arm64')
    expect(text).toContain('a rollback journal is present')
    expect(text).not.toContain('/Users/ada')

    // The kit answers the action by dismissing its toast; the copy answers for
    // itself, and that new toast offers no second copy.
    await vi.waitFor(() => expect(screen.getByRole('status').textContent).toContain('copied'))
    expect(screen.queryByRole('button', { name: 'Copy details' })).toBeNull()
  })

  it('says the clipboard refused, rather than doing nothing', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('clipboard refused'))
    const controller = createUpdateController({
      apply: vi.fn().mockRejectedValue(new Error(RAW)),
      about: () => Promise.resolve(ABOUT),
      clipboard: { writeText },
    })
    render(() => <ToastHost />)
    controller.showAvailable('0.8.1', NOTES, OFFER)
    fireEvent.click(screen.getByRole('button', { name: 'Install now' }))
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull(),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Copy details' }))
    await vi.waitFor(() =>
      expect(screen.getByRole('status').textContent).toContain(
        'Could not copy the update details: clipboard refused',
      ),
    )
    expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull()
  })

  it('is not offered anywhere but a failed update', () => {
    const controller = createUpdateController({
      finish: vi.fn(),
      about: () => Promise.resolve(ABOUT),
      clipboard: { writeText: vi.fn() },
    })
    render(() => <ToastHost />)
    // An available update, and the held state that asks to be finished: both
    // are outcomes of an update, and neither is a failure to diagnose.
    controller.showAvailable('0.8.1', NOTES, OFFER)
    expect(screen.queryByRole('button', { name: 'Copy details' })).toBeNull()
    controller.showHeldByCoordinator('0.8.0')
    expect(screen.queryByRole('button', { name: 'Copy details' })).toBeNull()
  })

  it('still copies when the build and the transaction state cannot be read', async () => {
    // Rule 3, the partial failures: the details are the point, and a read that
    // fails fills its field with `unknown` rather than failing the copy.
    const writeText = vi.fn().mockResolvedValue(undefined)
    const controller = createUpdateController({
      apply: vi.fn().mockRejectedValue(new Error(RAW)),
      about: () => Promise.reject(new Error('app.about: backend unreachable')),
      getState: () => Promise.reject(new Error('GetUpdateState: no updater')),
      clipboard: { writeText },
    })
    render(() => <ToastHost />)
    controller.showAvailable('0.8.1', NOTES, OFFER)
    fireEvent.click(screen.getByRole('button', { name: 'Install now' }))
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull(),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Copy details' }))
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
    const text = writeText.mock.calls[0][0] as string
    // The offer still names the build and the channel; the platform and the
    // journal are what could not be read.
    expect(text).toContain('build: 202611021101')
    expect(text).toContain('platform: unknown')
    expect(text).toContain('update state: unknown')
  })

  it('offers no action at all in a stand without a clipboard', async () => {
    const controller = failed()
    await vi.waitFor(() =>
      expect(document.querySelector('.ui-toast[data-level="danger"]')).not.toBeNull(),
    )
    expect(screen.queryByRole('button', { name: 'Copy details' })).toBeNull()
    expect(controller.state().kind).toBe('error')
  })
})
