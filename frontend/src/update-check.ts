/**
 * update-check — the one owner of what the last update check did
 * (nocx-1ebrs.36).
 *
 * 0.6.0 checked for updates at startup and every 24 hours, and swallowed
 * every failure in a bare catch: a dead check looked exactly like "no
 * update", which is how a shipped build went a whole release without
 * offering the next one. This module owns that outcome for both callers
 * (the startup check and the interval) so there is one state to read, not
 * two that agree until they don't: an available update still goes to the
 * notice, and a failure is kept with its cause — logged at warn and shown
 * in About — rather than dropped.
 */

import { createSignal, type Accessor } from 'solid-js'

import { CheckForUpdate } from '../bindings/github.com/shady2k/nocx/wailsapp'
import { log } from './log'
import type { UpdateController, UpdateOffer } from './update-controller'

/** The persisted setting's road into the window (nocx-1ebrs.35.1, ADR-0097):
 *  the channel is stored by the backend's settings store, and the check runs
 *  in the window — this bridge is the renderer handing the stored value
 *  across before each check, so no window-side copy of the setting can drift
 *  from the one the person set. */
export interface UpdateChannelBridge {
  /** Reads the persisted update.channel value; 'stable' when unset. */
  resolve: () => Promise<string>
  /** Tells the window which manifest the next check fetches. */
  apply: (channel: string) => Promise<void>
}

/** What the last SUCCESSFUL check found (nocx-1ebrs.55). A failure is the
 *  error accessor's to own, with its cause, so an outcome here never doubles
 *  as a quiet "nothing happened". */
export type UpdateCheckOutcome =
  { kind: 'up-to-date'; channel: string } | { kind: 'available'; version: string; notesUrl: string }

export interface UpdateCheckController {
  /** Why the last check failed, or null when it succeeded or has not run.
   *  An accessor, so the About card is live: the 24-hour re-check that
   *  succeeds clears a stale failure without anybody re-rendering by hand. */
  error: Accessor<string | null>
  /** What the last successful check found, or null before one succeeded.
   *  The channel is the one that check ran against — the stored setting the
   *  bridge resolved, not a window-side guess — so a page can say "channel
   *  nightly" and mean the manifest that was actually fetched. */
  lastOutcome: Accessor<UpdateCheckOutcome | null>
  /** The channel the last check ran against; `stable` before any check and
   *  where no bridge exists, which is that embedding's honest answer. */
  channel: Accessor<string>
  /** Run one check. Safe to call repeatedly; the interval does exactly that.
   *  Resolves what the check found, or null when it failed — the same truth
   *  the accessors hold, handed to a caller that awaits it (About's button,
   *  nocx-1ebrs.55). */
  run: () => Promise<UpdateCheckOutcome | null>
}

export function createUpdateCheck(
  notice: Pick<UpdateController, 'showAvailable'>,
  channel?: UpdateChannelBridge,
): UpdateCheckController {
  const [error, setError] = createSignal<string | null>(null)
  const [lastOutcome, setLastOutcome] = createSignal<UpdateCheckOutcome | null>(null)
  const [appliedChannel, setAppliedChannel] = createSignal('stable')

  /** The check that is in flight, if any. ONE OWNER FOR "one at a time"
   *  (nocx-1ebrs.66.1.1 review): the schedule throttles WHEN checks start,
   *  but About's button and a channel switch call `run()` directly, and two
   *  answers arriving out of order would let the later "no update" overwrite
   *  an offer the person can still see. Every caller shares this promise, so
   *  a second request joins the first rather than racing it. */
  let inFlight: Promise<UpdateCheckOutcome | null> | null = null

  const runOnce = async (): Promise<UpdateCheckOutcome | null> => {
    // The channel is resolved on EVERY run, not cached at startup: the
    // 24-hour interval and a person switching in Settings both read the
    // store's current truth through this one path.
    if (channel) {
      try {
        const resolved = await channel.resolve()
        await channel.apply(resolved)
        // Recorded only once it was applied: the channel a check RAN against
        // is what a page may quote, and a resolve whose apply failed never
        // ran a check at all.
        setAppliedChannel(resolved)
      } catch (err) {
        // A channel that could not be applied must not fall through to a
        // check against the wrong manifest: the whole point of the setting
        // is that it selects the channel (nocx-1ebrs.35.1). Surface it
        // through the same card a failed check uses — a silent wrong
        // channel is a soft degrade only a log ever saw.
        const reason = `update channel: ${err instanceof Error ? err.message : String(err)}`
        setError(reason)
        log.warn('update check failed', { reason })
        return null
      }
    }
    try {
      const info = await CheckForUpdate()
      setError(null)
      if (info) {
        // The build behind the offer, from the signed manifest the check
        // verified: the notice names it, and on nightly it is what
        // distinguishes an offered build from the one already running
        // (nocx-vh2hr.1.5).
        const offer: UpdateOffer = {
          channel: info.Channel,
          build: { commit: info.Commit, builtAt: info.BuiltAt },
        }
        notice.showAvailable(info.Version, info.NotesURL, offer)
        const found: UpdateCheckOutcome = {
          kind: 'available',
          version: info.Version,
          notesUrl: info.NotesURL,
        }
        setLastOutcome(found)
        return found
      }
      const current: UpdateCheckOutcome = { kind: 'up-to-date', channel: appliedChannel() }
      setLastOutcome(current)
      return current
    } catch (err) {
      const reason = err instanceof Error ? err.message : String(err)
      setError(reason)
      log.warn('update check failed', { reason })
      return null
    }
  }

  const run = (): Promise<UpdateCheckOutcome | null> => {
    if (inFlight) return inFlight
    inFlight = runOnce().finally(() => {
      inFlight = null
    })
    return inFlight
  }

  return { error, lastOutcome, channel: appliedChannel, run }
}
