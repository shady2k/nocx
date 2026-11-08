/**
 * update-details — what a failed update's "Copy details" puts on the clipboard
 * (nocx-vh2hr.1.16, the owner's decision of 2026-11-02).
 *
 * The failure toast says a safe sentence and always will: a filesystem errno or
 * a path in a notification is noise nobody asked for. But diagnosing the
 * owner's own failed macOS update took a Terminal relaunch to read
 * "download write: context deadline exceeded", because the renderer showed only
 * "The update could not finish…" — the cause existed, in a log, where the
 * person could not reach it.
 *
 * So the toast stays the safe sentence and carries one action that copies the
 * facts underneath it: the backend's own error, which step failed, the version
 * and build it was about, the channel, the platform, and whether a rollback
 * journal explains the state. Plain text, one fact per line, in the order a
 * bug report wants them — the same shape the About page's copy already uses
 * (frontend/src/about-section.tsx), because these land in issue trackers and
 * chat alike.
 *
 * THE DETAILS GET PASTED IN PUBLIC. So the text is assembled from named fields
 * and nothing else: no window state, no typed input, no token, no request body.
 * The one path a refusal may name is the managed debris bundle, which the
 * person needs to clear it, and the HOME DIRECTORY is replaced by `~` wherever
 * it appears — inside the error as much as beside it. Everything else is a
 * path the product itself named.
 */

import { managedDebrisPath } from './update-failure'

/** The running build's own identity, as `app.about` states it over JSON-RPC
 *  (contracts/app.about.schema.json). Only the two fields the details quote are
 *  named here; what the build reports about itself is About's copy, not this
 *  module's. */
export interface RunningBuild {
  build: string
  platform: string
}

/** What the window can say about an update transaction at the moment details
 *  are copied — the fields of the `GetUpdateState` binding this surface reads.
 *  `Pending` is a rollback journal whose exchange happened and which nothing
 *  has certified or rolled back yet (internal/update, `State`). */
export interface UpdateStateReading {
  Pending?: boolean
  HeldByOlderCoordinator?: boolean
  Downloading?: boolean
  Downloaded?: number
  Total?: number
}

/** What the download has done so far, as the notice and the copied details
 *  both state it. */
export interface DownloadProgress {
  downloaded: number
  total: number
}

export interface UpdateFailureFacts {
  /** The binding the person's click reached: apply, restart or finish. */
  stage: string
  /** The backend's own error message, unedited. */
  error: string
  /** The version the failed update was about. */
  version: string
  /** Its build number (YYYYMMDDHHMM), or the commit, or `unknown`. */
  build: string
  /** The channel the offer came from, or `unknown`. */
  channel: string
  /** GOOS/GOARCH of the running build (`darwin/arm64`), or `unknown`. */
  platform: string
  /** One phrase for the update state, from [updateStateLine]. */
  state: string
}

/**
 * The home directory a path names, replaced by `~`.
 *
 * A home directory is the one part of a path that identifies a person, and it
 * is the part a pasted log leaks; the rest of the path is what the product
 * itself named and is what makes the report useful. `/Users/<name>` and
 * `/home/<name>` are the two roots the shipped platforms use; a Windows
 * `C:\Users\<name>` is covered for the same reason, though nothing here ships
 * there yet. Only the name is replaced — the separator and everything after it
 * stay, so `~/Library/Caches/nocx/update.zip` still reads as a path.
 */
export function redactHome(text: string): string {
  return text
    .replace(/\/Users\/[^/\\\s"'`]+/g, '~')
    .replace(/\/home\/[^/\\\s"'`]+/g, '~')
    .replace(/[A-Za-z]:\\Users\\[^\\\s"'`]+/g, '~')
}

/** The notice's words for a download in flight (nocx-vh2hr.1.15):
 *  "Downloading 42% · 63 of 150 MB". Decimal megabytes, because the manifest
 *  declares bytes and every release page quotes the same size; the percent is
 *  floored so it never reads 100% before the last byte.
 *
 *  ONE OWNER for those words: the notice prints them while the body arrives,
 *  and the copied details print them when a download is what failed. */
export function downloadProgressLabel(progress: DownloadProgress): string {
  if (progress.total <= 0) return 'Downloading…'
  const figures = downloadFigures(progress)
  return `Downloading ${figures.percent}% · ${figures.done} of ${figures.total} MB`
}

/** The two numbers a download's figure is made of: the floored percent and
 *  the decimal megabytes. ONE calculator for them (nocx-vh2hr.1.25): the
 *  notice's figure is the catalogue's words around these numbers, the copied
 *  details say the English sentence with the same ones, and a second division
 *  would eventually round a different way. */
export function downloadFigures(progress: DownloadProgress): {
  percent: number
  done: string
  total: string
} {
  const percent =
    progress.total > 0
      ? Math.min(100, Math.floor((Math.max(0, progress.downloaded) * 100) / progress.total))
      : 0
  return { percent, done: megabytes(progress.downloaded), total: megabytes(progress.total) }
}

function megabytes(bytes: number): string {
  return (Math.max(0, bytes) / 1_000_000).toFixed(0)
}

/** What the update state line says: the fact a person can act on, not the
 *  struct. `Pending` is the journal, and a journal that is present is the
 *  difference between "the update stopped half way" and "nothing happened". */
export function updateStateLine(reading: UpdateStateReading): string {
  if (reading.Downloading === true) {
    const total = reading.Total ?? 0
    const downloaded = Math.max(0, reading.Downloaded ?? 0)
    // The notice's own words, unchanged: one phrasing for one fact.
    return total > 0
      ? downloadProgressLabel({ downloaded, total })
      : downloadProgressLabel({ downloaded: 0, total: 0 })
  }
  if (reading.Pending === true && reading.HeldByOlderCoordinator === true)
    return 'a rollback journal is present; the update waits for an older coordinator'
  if (reading.Pending === true) return 'a rollback journal is present'
  return 'no journal; nothing is pending'
}

/** The whole text, one fact per line. Every value is a named field; the error
 *  and the debris path are the only free-form ones, and both go through
 *  [redactHome] because they come from the backend's own sentences. */
export function updateFailureDetails(facts: UpdateFailureFacts): string {
  const debris = managedDebrisPath(facts.error)
  const lines = [
    'nocx update failure',
    `stage: ${facts.stage}`,
    `error: ${facts.error}`,
    `version: ${facts.version}`,
    `build: ${facts.build}`,
    `channel: ${facts.channel}`,
    `platform: ${facts.platform}`,
    `update state: ${facts.state}`,
  ]
  if (debris !== null) lines.push(`debris: ${debris}`)
  return redactHome(lines.join('\n'))
}
