import { createSignal } from 'solid-js'
import {
  ApplyUpdate,
  FinishPendingUpdate,
  GetUpdateState,
  RestartWindow,
} from '../bindings/github.com/shady2k/nocx/wailsapp'
import { showToast, updateToast, dismissToast, type ToastOptions } from './ui/toast'
import { log } from './log'
import { updateFailureMessage } from './update-failure'
import {
  downloadFigures,
  updateFailureDetails,
  updateStateLine,
  type DownloadProgress,
  type RunningBuild,
  type UpdateStateReading,
} from './update-details'
import type { ClipboardAccess } from './clipboard'
import { t } from './i18n/locale-model'

/** What an offer says about the build behind it (nocx-vh2hr.1.5).
 *
 *  A nightly of the version a person already runs is still a different build
 *  (ADR-79b6cy61: the version comes from the last stable tag), so the notice
 *  needs more than the version to mean anything. Both values come from the
 *  signed manifest the check verified; empty strings are an older manifest
 *  that named no build, and the notice then says only what it can. */
/** What the window's update state offers a surface that asks: the held pair a
 *  restore needs, and the bytes a download is moving. */
interface DownloadReading extends UpdateStateReading {
  Downloading?: boolean
  Downloaded?: number
  Total?: number
}

/** How often the notice reads the download's progress. A poll against a
 *  binding, in the same shape the held-pair restore already uses. */
const DOWNLOAD_PROGRESS_POLL_MS = 250

export interface UpdateOffer {
  /** The channel whose signed manifest offered the build. Only `nightly`
   *  changes the notice's words and link. */
  channel: string
  /** The offered build's stamped identity. */
  build: { commit: string; builtAt: string; number: string }
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
  | { kind: 'downloading'; version: string; progress?: DownloadProgress }
  | { kind: 'pending'; version: string }
  | { kind: 'held'; version: string }
  | { kind: 'finishing'; version: string }
  | {
      kind: 'error'
      message: string
      retry:
        AvailableOffer | { kind: 'held'; version: string } | { kind: 'pending'; version: string }
    }

/** Which binding the person's click reached — the step a failure is reported
 *  against, the same word the backend logs (`logUpdateFailure` in main.go). */
type UpdateFailureStage = 'apply' | 'restart' | 'finish'

type UpdateFailureRetry = Extract<UpdateState, { kind: 'available' | 'held' | 'pending' }>

export interface UpdateControllerDeps {
  apply?: () => Promise<void>
  /** Where the download's progress is read from: the window's own update
   *  state, the same binding the notice already consults. */
  getState?: () => Promise<DownloadReading>
  finish?: () => void | Promise<void>
  restart?: () => void | Promise<void>
  /** Where a failed update's "Copy details" writes (nocx-vh2hr.1.16). The
   *  product's own clipboard seam (frontend/src/clipboard.ts), injected like
   *  every other platform capability: a stand without one then offers no
   *  action rather than a button that does nothing. */
  clipboard?: Pick<ClipboardAccess, 'writeText'>
  /** The running build, as `app.about` states it: the details quote its
   *  platform, and its build number stands in when the failed update named
   *  none. Absent in an embedding without a backend, where the details say
   *  `unknown`. */
  about?: () => Promise<RunningBuild>
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
    getState?: () => Promise<DownloadReading & { HeldByOlderCoordinator: boolean; Version: string }>
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
  // THE BUILD NUMBER FIRST (nocx-vh2hr.1.5, the owner's decision of 2026-10-30):
  // every build carries YYYYMMDDHHMM beside the version, monotonic across
  // reruns and preserved by promotion, and it is the number macOS carries in
  // CFBundleVersion — so the notice names the same build the bundle does. The
  // commit and the build time remain the fallback for a manifest whose instant
  // cannot be read; the commit is the last resort because two nightlies can
  // share one when nothing rebuilt.
  const number = offer.build.number.trim()
  if (/^[0-9]{12}$/.test(number)) return number
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

const STABLE_OFFER: UpdateOffer = {
  channel: 'stable',
  build: { commit: '', builtAt: '', number: '' },
}

/** One owner for update state and its one kit surface. The sidebar is rebuilt
 * on reconnect; this controller lives for the whole window run (AD-8). */
export function createUpdateController(deps: UpdateControllerDeps = {}): UpdateController {
  const [state, setState] = createSignal<UpdateState>({ kind: 'hidden' })
  const applyUpdate = deps.apply ?? (() => ApplyUpdate())
  const finishUpdate = deps.finish ?? (() => FinishPendingUpdate())
  const restartWindow = deps.restart ?? (() => RestartWindow())
  let toastId: number | null = null
  /** The failure the "Copy details" action reads: which binding the person's
   *  click reached, the backend's own words, and the state to retry from. Kept
   *  out of the visible state — the toast and the indicator carry the safe
   *  sentence, and the cause belongs behind the action (nocx-vh2hr.1.16). */
  let lastFailure: { stage: UpdateFailureStage; error: string; retry: UpdateFailureRetry } | null =
    null
  // Per-run memory, never localStorage: both Later and the kit close button
  // silence the announcement, not the persistent indicator.
  const announced = new Set<string>()

  const present = (options: ToastOptions): void => {
    if (toastId !== null) dismissToast(toastId)
    toastId = showToast({ ...options, duration: 0 })
  }

  /** Repaint the notice this controller already owns, keeping its element,
   *  its timer and its actions. A download ticks every quarter second
   *  (nocx-vh2hr.1.25): dismissing and re-raising on each one would restart
   *  the enter animation, re-announce the whole sentence to a screen reader,
   *  and re-create the bar instead of moving it. A toast that is gone is not
   *  resurrected here — [present] is what raises one. */
  const repaint = (options: Pick<ToastOptions, 'message' | 'progress'>): void => {
    if (toastId !== null) updateToast(toastId, options)
  }

  /** The download's notice, from the bytes the backend reports. The bar is
   *  drawn only when there IS a fraction — the kit's ProgressBar is
   *  determinate and says so, so 0 of 0 renders no bar rather than one frozen
   *  at zero (which reads as "stalled"). The figure beside it is the
   *  catalogue's words around the two numbers update-details computes. */
  const downloadingToast = (version: string, progress?: DownloadProgress): ToastOptions => {
    const message = t('update.downloading', 'Downloading nocx {version}…', { version })
    if (progress === undefined || progress.total <= 0) return { message }
    const figures = downloadFigures(progress)
    return {
      message,
      progress: {
        value: progress.downloaded / progress.total,
        label: t('update.downloading.figure', '{percent}% · {done} of {total} MB', figures),
        ariaLabel: t('update.downloading.aria', 'Update download'),
      },
    }
  }

  /** The running build the details quote. A failed read is not a failed copy:
   *  the fields it would have filled say `unknown`, which is the honest answer
   *  and still worth pasting. */
  const runningBuild = async (): Promise<RunningBuild> => {
    const unknown: RunningBuild = { build: 'unknown', platform: 'unknown' }
    if (!deps.about) return unknown
    try {
      const about = await deps.about()
      return { build: about.build, platform: about.platform }
    } catch (error) {
      log.warn('reading the running build for update details failed', {
        error: error instanceof Error ? error.message : String(error),
      })
      return unknown
    }
  }

  /** The transaction's own state at the moment of copying — `Pending` is the
   *  rollback journal. `null` when the read failed, which the details say as
   *  `unknown` rather than inventing "nothing is pending". */
  const transactionState = async (): Promise<UpdateStateReading | null> => {
    try {
      return await (deps.getState ?? (() => GetUpdateState()))()
    } catch {
      return null
    }
  }

  /** Everything the copied text holds, from the failure and the window's own
   *  facts. No user input, no window content, no token: the fields are named
   *  here and nothing else is read (frontend/src/update-details.ts). */
  const failureDetailsText = async (failure: NonNullable<typeof lastFailure>): Promise<string> => {
    const about = await runningBuild()
    const retry = failure.retry
    const offer = retry.kind === 'available' ? retry.offer : null
    const reading = await transactionState()
    return updateFailureDetails({
      stage: failure.stage,
      error: failure.error,
      version: retry.version,
      build: offer ? offeredBuildLabel(offer) : about.build,
      channel: offer ? offer.channel : 'unknown',
      platform: about.platform,
      state: reading === null ? 'unknown' : updateStateLine(reading),
    })
  }

  /** The failed toast's one action. The kit dismisses the toast when an action
   *  runs, so the answer is a toast of its own: what happened to the copy. */
  const copyFailureDetails = async (): Promise<void> => {
    const failure = lastFailure
    const clipboard = deps.clipboard
    if (!failure || !clipboard) return
    try {
      await clipboard.writeText(await failureDetailsText(failure))
      present({ message: t('update.details.copied', 'Update details copied') })
    } catch (error) {
      // A clipboard is a platform capability that can refuse — a non-secure
      // context, a platform that declined the write — and a button that
      // silently does nothing is worse than one that is not there.
      present({
        level: 'danger',
        message: t('update.details.copyFailed', 'Could not copy the update details: {error}', {
          error: error instanceof Error ? error.message : String(error),
        }),
      })
    }
  }
  const fail = (
    error: unknown,
    retry: Extract<UpdateState, { kind: 'available' | 'held' | 'pending' }>,
    stage: UpdateFailureStage,
  ): void => {
    const raw = error instanceof Error ? error.message : String(error)
    log.warn('update action failed', { error: raw })
    const message = updateFailureMessage(error)
    setState({ kind: 'error', message, retry })
    lastFailure = { stage, error: raw, retry }
    // THE SAFE SENTENCE STAYS THE SENTENCE (nocx-vh2hr.1.16): the toast is what
    // a person glancing at it must be able to read, and the cause is one click
    // away rather than in a log they cannot reach. No clipboard seam, no
    // action — a stand without a platform clipboard offers the sentence alone.
    present({
      level: 'danger',
      message,
      ...(deps.clipboard && deps.about
        ? {
            actions: [
              {
                label: t('update.details.copy', 'Copy details'),
                onClick: () => void copyFailureDetails(),
              },
            ],
          }
        : {}),
    })
  }
  const restart = (): void => {
    const current = state()
    if (current.kind !== 'pending') return
    setState({ kind: 'finishing', version: current.version })
    showOffer()
    void Promise.resolve()
      .then(restartWindow)
      .catch((error: unknown) => fail(error, current, 'restart'))
  }
  const finish = (): void => {
    const current = state()
    if (current.kind !== 'held') return
    setState({ kind: 'finishing', version: current.version })
    showOffer()
    void Promise.resolve()
      .then(finishUpdate)
      .catch((error: unknown) => fail(error, current, 'finish'))
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
                link: {
                  label: nightly
                    ? t('update.link.whatChanged', 'What changed')
                    : t('update.link.releaseNotes', 'Release notes'),
                  href: current.notesUrl,
                },
              }),
          actions: [
            { label: installLabel, variant: 'primary', onClick: () => void apply() },
            { label: t('update.later', 'Later'), onClick: () => {} },
          ],
        })
        return
      }
      case 'finishing':
        present({
          message:
            t('update.finishingHead', 'Finishing the update to nocx') + ` ${current.version}…`,
        })
        return
      case 'downloading':
        present(downloadingToast(current.version, current.progress))
        return
      case 'pending':
        present({
          level: 'success',
          message:
            t('update.installedRestart.head', 'nocx') +
            ` ${current.version} ` +
            t('update.installedRestart.tail', 'installed — restart to apply'),
          actions: [
            { label: t('update.restartNow', 'Restart now'), variant: 'primary', onClick: restart },
          ],
        })
        return
      case 'held':
        present({
          level: 'warning',
          // The words say what the action does (review, nocx-1ebrs.66.1.1):
          // finishing stops the coordinator this window has outlived and
          // QUITS — it does not relaunch, so calling it a restart told the
          // person to expect a window that would not appear.
          message:
            t('update.installedClose.head', 'nocx') +
            ` ${current.version} ` +
            t(
              'update.installedClose.tail',
              'installed — close nocx and open it again to finish (this closes open sessions)',
            ),
          actions: [
            { label: t('update.closeNocx', 'Close nocx'), variant: 'primary', onClick: finish },
          ],
        })
        return
    }
  }
  const apply = async (): Promise<void> => {
    const current = state()
    if (current.kind !== 'available') return
    setState({ kind: 'downloading', version: current.version })
    showOffer()
    // THE AUTHORITATIVE SYNC OF PROGRESS IS THE BACKEND'S STATE
    // (nocx-vh2hr.1.15): the download's bytes are the updater's, and this polls
    // the same binding the notice already consults — no new channel, no
    // renderer-side guess. It re-presents only when the percent moves, so the
    // toast is a progress line rather than a flicker.
    const stop = followDownload(current.version)
    try {
      await applyUpdate()
      stop()
      setState({ kind: 'pending', version: current.version })
      showOffer()
    } catch (error) {
      stop()
      fail(error, current, 'apply')
    }
  }

  /** followDownload polls the window's update state while an apply is in
   *  flight and returns the stop. Percent steps only: a repaint per byte would
   *  be a toast nobody can read and a renderer doing nothing else — and the
   *  repaint is IN PLACE (repaint), so a step moves the bar rather than
   *  replacing the notice. The first read happens at once: the state carries
   *  the declared size from the moment the body is asked for, and the bar is
   *  there with the message rather than a quarter second later
   *  (nocx-vh2hr.1.25). */
  const followDownload = (version: string): (() => void) => {
    const getState = deps.getState ?? (() => GetUpdateState())
    let stopped = false
    let shown = -1
    const tick = async (): Promise<void> => {
      if (stopped) return
      try {
        const reading = await getState()
        if (stopped) return
        if (reading.Downloading !== true) return
        const progress = { downloaded: reading.Downloaded ?? 0, total: reading.Total ?? 0 }
        const percent =
          progress.total > 0 ? Math.floor((progress.downloaded * 100) / progress.total) : 0
        if (percent === shown) return
        shown = percent
        const current = state()
        if (current.kind !== 'downloading') return
        setState({ kind: 'downloading', version, progress })
        repaint(downloadingToast(version, progress))
      } catch {
        // A state read that fails is not the download failing: the apply's own
        // outcome decides, and the notice keeps saying what it last knew.
      }
    }
    void tick()
    const timer = setInterval(() => void tick(), DOWNLOAD_PROGRESS_POLL_MS)
    return () => {
      stopped = true
      clearInterval(timer)
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
