// The far helper's axis, folded once (ADR-727g63tr decision 2; design
// §5.10). The head and the Git panel both draw THIS answer — a second
// formulation is a second owner of one fact, which the connection-owner
// record refuses.
//
// IT ADDS NOTHING TO THE BACKEND'S ANSWER. A sentence is a person's words for
// the state and reason the contract carried, the tone is presentation, and the
// action is the member's own `action`: `refresh` exists only where the wire
// said `refresh`, `host-settings` only where it said `host-settings`. An
// `install-failed` connection offers no button (owner, 2026-11-16), and
// `helper-lost` with `none` offers none either — the fold cannot invent a
// remedy the backend did not name.
//
// THE SENTENCES NEVER SPEAK THE CONTRACT'S TOKENS. `mode-script`,
// `helper-lost` and the rest are the wire's vocabulary; a person reads
// "Script integration", "the helper stopped". A spec asserts this, because a
// surface that leaks `mode-script` is the prose-matching defect wearing a
// different coat.

import type { PaneConnection } from '../generated/pane.connection'
import { t } from '../i18n/locale-model'

type HelperAxisAction = 'refresh' | 'host-settings'
type HelperAxisTone = 'muted' | 'warning'

export interface HelperAxis {
  /** The contract's own state, so a surface can branch without re-reading
   *  the observation. */
  readonly state: 'starting' | 'serving' | 'paused' | 'unavailable'
  /** What a person reads. Null only for `serving`: a helper that serves the
   *  connection has nothing to explain (design §5.10). */
  readonly sentence: string | null
  readonly tone: HelperAxisTone
  /** The backend's action and nothing besides it. Null when the wire sent
   *  none. */
  readonly action: HelperAxisAction | null
}

/** The axis for one connection's helper member, or null when the connection
 *  has not answered yet. Null is "nothing told, nothing claimed" — the same
 *  rule the head follows for a missing observation (ADR-ba6xet1b): a
 *  connection the backend has not described has no state to draw, and the
 *  fold may not mint one. */
export function helperAxis(helper: PaneConnection['helper'] | null | undefined): HelperAxis | null {
  if (helper === null || helper === undefined) return null
  const action = helper.action === 'none' ? null : helper.action
  switch (helper.state) {
    case 'starting':
      return {
        state: 'starting',
        sentence: t('helper.starting', 'Preparing the nocx helper'),
        tone: 'muted',
        action,
      }
    case 'serving':
      return { state: 'serving', sentence: null, tone: 'muted', action }
    case 'paused':
      return {
        state: 'paused',
        sentence: t(
          'helper.paused',
          'The connection is paused, so the nocx helper is not reachable.',
        ),
        tone: 'warning',
        action,
      }
    case 'unavailable':
      return {
        state: 'unavailable',
        sentence: sentenceFor(helper.reason),
        tone: 'warning',
        action,
      }
  }
}

/** One sentence per reason. The reason is a closed enum in the contract, so
 *  an exhaustive switch is the completeness check: a reason added to the wire
 *  fails the type check here rather than reaching a window as an empty line. */
function sentenceFor(
  reason: Extract<PaneConnection['helper'], { state: 'unavailable' }>['reason'],
): string {
  switch (reason) {
    case 'mode-raw':
      return t('helper.modeRaw', 'This host uses Raw integration, so the nocx helper is not used.')
    case 'mode-script':
      return t(
        'helper.modeScript',
        'This host uses Script integration, so the nocx helper is not used.',
      )
    case 'helper-lost':
      return t('helper.lost', 'The nocx helper stopped on this connection.')
    case 'consent-pending':
      return t('helper.consentPending', 'The nocx helper is waiting for permission on this host.')
    case 'consent-denied':
      return t('helper.consentDenied', 'The nocx helper is not allowed on this host.')
    case 'unsupported-platform':
      return t('helper.unsupportedPlatform', 'This host cannot run the nocx helper.')
    case 'install-failed':
      return t('helper.installFailed', 'The nocx helper could not be installed on this host.')
    case 'exec-forbidden':
      return t('helper.execForbidden', 'The host refused to run the nocx helper.')
    case 'version-mismatch':
      return t('helper.versionMismatch', 'The nocx helper on this host is a different version.')
    case 'connection':
      return t('helper.connection', 'The nocx helper is unavailable while the connection is down.')
  }
}

/** The saved connection a tab was opened from, or null when it has none.
 *
 *  ONE ANSWER, TWO SPELLINGS. Quick Connect and a hand-typed host open a pane
 *  with `profileId: ''` (main.tsx's newSSHPane('', …)), while a local tab has
 *  no profile at all; both mean "there is no host record this tab may edit".
 *  A surface that tests the raw option for null alone draws a door that opens
 *  nothing on the ad-hoc tab — the defect ALX-998 found — so every surface and
 *  the pane itself ask here. */
export function savedProfileId(profileId: string | null | undefined): string | null {
  return profileId === null || profileId === undefined || profileId === '' ? null : profileId
}

/** The axis as an UNSAVED tab must read it.
 *
 *  The backend offers `host-settings` for a host whose mode can be changed on
 *  a saved connection's record. A tab opened from Quick Connect has no such
 *  record, and the head and the Git panel draw the same substitute: the
 *  sentence saying what to do, with the action taken away. Kept here, beside
 *  the axis, because a surface that folded it itself would be the second
 *  formulation of one answer. */
export function withSavedConnection(
  axis: HelperAxis | null,
  profileId: string | null | undefined,
): HelperAxis | null {
  if (axis === null || axis.action !== 'host-settings' || savedProfileId(profileId) !== null) {
    return axis
  }
  return {
    ...axis,
    sentence: `${axis.sentence ?? ''} ${t(
      'helper.saveConnection',
      'Save this connection to change how nocx integrates with its host.',
    )}`.trim(),
    action: null,
  }
}
