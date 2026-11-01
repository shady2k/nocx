import { createSignal } from 'solid-js'
import {
  ApplyUpdate,
  FinishPendingUpdate,
  GetUpdateState,
  RestartWindow,
} from '../bindings/github.com/shady2k/nocx/wailsapp'
import { showToast, dismissToast, type ToastOptions } from './ui/toast'
import { log } from './log'
import { updateFailureMessage } from './update-failure'

/** What an offer says about the build behind it (nocx-vh2hr.1.5).
 *
 *  A nightly of the version a person already runs is still a different build
 *  (ADR-79b6cy61: the version comes from the last stable tag), so the notice
 *  needs more than the version to mean anything. Both values come from the
 *  signed manifest the check verified; empty strings are an older manifest
 *  that named no build, and the notice then says only what it can. */
export interface UpdateOffer {
  /** The channel whose signed manifest offered the build. Only `nightly`
   *  changes the notice's words and link. */
  channel: string
  /** The offered build's stamped identity. */
  build: { commit: string; builtAt: string }
}

type AvailableOffer = {
  kind: 'available'
  version: string
  notesUrl: string
  offer: UpdateOffer
}

type UpdateState =
  | { kind: 'hidden' }
  | AvailableOffer
  | { kind: 'downloading'; version: string }
  | { kind: 'pending'; version: string }
  | { kind: 'held'; version: string }
  | { kind: 'finishing'; version: string }
  | {
      kind: 'error'
      message: string
      retry:
        AvailableOffer | { kind: 'held'; version: string } | { kind: 'pending'; version: string }
    }

export interface UpdateControllerDeps {
  apply?: () => Promise<void>
  finish?: () => void | Promise<void>
  restart?: () => void | Promise<void>
}

export interface UpdateController {
  state: () => UpdateState
  /** Announce an available update. `offer` is the build behind it; without
   *  one the announcement is the stable-shaped one it has always been. */
  showAvailable: (version: string, notesUrl: string, offer?: UpdateOffer) => void
  showOffer: () => void
  apply: () => Promise<void>
  showHeldByCoordinator: (version: string) => void
  restore: (deps?: {
    getState?: () => Promise<{ HeldByOlderCoordinator: boolean; Version: string }>
    /** Called when the read failed. The composition root decides whether a
     *  person is told (the desktop window warns; a browser stand has no
     *  updater to warn about). */
    onFailure?: (reason: string) => void
  }) => Promise<void>
}

/** The shortest honest name for an offered build: the commit's first seven
 *  characters (the same short sha About shows), or the built-at stamp when the
 *  manifest named no commit. Empty when it named neither — then the version is
 *  all the notice can say, and it must not invent a build (nocx-vh2hr.1.5). */
function offeredBuildLabel(offer: UpdateOffer): string {
  const commit = offer.build.commit.trim()
  if (/^[0-9a-f]{40}$/.test(commit)) return commit.slice(0, 7)
  const builtAt = offer.build.builtAt.trim()
  return builtAt === '' ? '' : builtAt.slice(0, 16).replace('T', ' ')
}

/** The key an announcement is remembered by. Stable keys on the version;
 *  nightly keys on the build, because two nightlies of one version are two
 *  offers and the second must not be swallowed as the first
 *  (nocx-vh2hr.1.5). */
function offerKey(version: string, offer: UpdateOffer): string {
  if (offer.channel !== 'nightly') return version
  return `${version} ${offeredBuildLabel(offer)}`
}

const STABLE_OFFER: UpdateOffer = { channel: 'stable', build: { commit: '', builtAt: '' } }

/** One owner for update state and its one kit surface. The sidebar is rebuilt
 * on reconnect; this controller lives for the whole window run (AD-8). */
export function createUpdateController(deps: UpdateControllerDeps = {}): UpdateController {
  const [state, setState] = createSignal<UpdateState>({ kind: 'hidden' })
  const applyUpdate = deps.apply ?? (() => ApplyUpdate())
  const finishUpdate = deps.finish ?? (() => FinishPendingUpdate())
  const restartWindow = deps.restart ?? (() => RestartWindow())
  let toastId: number | null = null
  // Per-run memory, never localStorage: both Later and the kit close button
  // silence the announcement, not the persistent indicator.
  const announced = new Set<string>()

  const present = (options: ToastOptions): void => {
    if (toastId !== null) dismissToast(toastId)
    toastId = showToast({ ...options, duration: 0 })
  }
  const fail = (
    error: unknown,
    retry: Extract<UpdateState, { kind: 'available' | 'held' | 'pending' }>,
  ): void => {
    log.warn('update action failed', {
      error: error instanceof Error ? error.message : String(error),
    })
    const message = updateFailureMessage(error)
    setState({ kind: 'error', message, retry })
    present({ level: 'danger', message })
  }
  const restart = (): void => {
    const current = state()
    if (current.kind !== 'pending') return
    setState({ kind: 'finishing', version: current.version })
    showOffer()
    void Promise.resolve()
      .then(restartWindow)
      .catch((error: unknown) => fail(error, current))
  }
  const finish = (): void => {
    const current = state()
    if (current.kind !== 'held') return
    setState({ kind: 'finishing', version: current.version })
    showOffer()
    void Promise.resolve()
      .then(finishUpdate)
      .catch((error: unknown) => fail(error, current))
  }
  const showOffer = (installLabel = 'Install'): void => {
    const before = state()
    if (before.kind === 'error') setState(before.retry)
    const current = state()
    switch (current.kind) {
      case 'hidden':
        return
      case 'available': {
        const nightly = current.offer.channel === 'nightly'
        const build = offeredBuildLabel(current.offer)
        // A nightly of the installed version is the normal nightly case
        // (ADR-79b6cy61), and the version alone would claim the person is
        // being offered what they already run — so the build is named. Stable
        // is unchanged: its version is the offer (nocx-vh2hr.1.5).
        const message =
          nightly && build !== ''
            ? `nocx ${current.version}, build ${build} is available`
            : `nocx ${current.version} is available`
        present({
          message,
          ...(current.notesUrl === ''
            ? {}
            : {
                // The nightly link is a commit range, not notes: label it for
                // what it shows. The backend composes it, and leaves it empty
                // when there is no range to show.
                link: { label: nightly ? 'What changed' : 'Release notes', href: current.notesUrl },
              }),
          actions: [
            { label: installLabel, variant: 'primary', onClick: () => void apply() },
            { label: 'Later', onClick: () => {} },
          ],
        })
        return
      }
      case 'finishing':
        present({ message: `Finishing the update to nocx ${current.version}…` })
        return
      case 'downloading':
        present({ message: `Downloading nocx ${current.version}…` })
        return
      case 'pending':
        present({
          level: 'success',
          message: `nocx ${current.version} installed — restart to apply`,
          actions: [{ label: 'Restart now', variant: 'primary', onClick: restart }],
        })
        return
      case 'held':
        present({
          level: 'warning',
          // The words say what the action does (review, nocx-1ebrs.66.1.1):
          // finishing stops the coordinator this window has outlived and
          // QUITS — it does not relaunch, so calling it a restart told the
          // person to expect a window that would not appear.
          message: `nocx ${current.version} installed — close nocx and open it again to finish (this closes open sessions)`,
          actions: [{ label: 'Close nocx', variant: 'primary', onClick: finish }],
        })
        return
    }
  }
  const apply = async (): Promise<void> => {
    const current = state()
    if (current.kind !== 'available') return
    setState({ kind: 'downloading', version: current.version })
    showOffer()
    try {
      await applyUpdate()
      setState({ kind: 'pending', version: current.version })
      showOffer()
    } catch (error) {
      fail(error, current)
    }
  }
  const showHeldByCoordinator = (version: string): void => {
    setState({ kind: 'held', version })
    // A late startup read must retire any announcement already showing.
    if (toastId !== null) dismissToast(toastId)
    toastId = null
  }
  return {
    state,
    showOffer,
    apply,
    showAvailable: (version, notesUrl, offer) => {
      const current = state()
      if (
        current.kind === 'downloading' ||
        current.kind === 'pending' ||
        current.kind === 'held' ||
        current.kind === 'finishing' ||
        (current.kind === 'error' && current.retry.kind !== 'available')
      )
        return
      const resolved = offer ?? STABLE_OFFER
      setState({ kind: 'available', version, notesUrl, offer: resolved })
      const key = offerKey(version, resolved)
      if (!announced.has(key)) {
        announced.add(key)
        showOffer('Install now')
      }
    },
    showHeldByCoordinator,
    restore: async (
      deps: {
        getState?: () => Promise<{ HeldByOlderCoordinator: boolean; Version: string }>
        onFailure?: (reason: string) => void
      } = {},
    ) => {
      const getState = deps.getState ?? (() => GetUpdateState())
      try {
        const restored = await getState()
        if (!restored.HeldByOlderCoordinator) return
        showHeldByCoordinator(restored.Version)
      } catch (error) {
        // The log keeps the cause; the PERSON gets the fact. A failed read
        // leaves the indicator hidden, which reads as "no update" from a
        // window that never looked — the soft degrade AGENTS.md says must be
        // visible in the product and not only in a log (review,
        // nocx-1ebrs.66.1.1).
        log.warn('reading update state failed', {
          error: error instanceof Error ? error.message : String(error),
        })
        // The COMPOSITION ROOT decides whether this is worth a person's
        // attention: in a browser or the e2e stand there is no updater at
        // all, so a warning there is noise a spec then trips over. The
        // desktop window passes a callback that says it; anything else
        // leaves the log as the record.
        deps.onFailure?.(error instanceof Error ? error.message : String(error))
      }
    },
  }
}
