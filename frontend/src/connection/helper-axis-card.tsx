// The far helper's axis, drawn ONCE (ADR-727g63tr decision 2; design §5.10).
// The Git panel had this card first; Files needs the same card, and a second
// copy in Files is the defect the connection-owner record exists to stop: two
// surfaces agreeing today and disagreeing the first time one of them learns a
// new reason.
//
// IT DRAWS THE ONE FOLD, AND NOTHING BESIDE IT. The sentence, the tone and the
// action are `helperAxis`'s answer, folded for the tab's saved connection by
// `withSavedConnection` — the same call the pane's head makes — so the panel, the
// head and the drop gesture cannot spell the same refusal twice. A surface says
// only two things of its own: its identity (the test ids it is addressed by) and
// the headline that names what it needs the helper for.
//
// TWO STATES, ONE COMPONENT. `starting` — and an observation that has not
// arrived at all, which the gate answers the same way — is a NEUTRAL waiting
// card, never a warning: every healthy SSH connection spends a moment there,
// and a warning on each of them is a warning nobody reads by the time one is
// true. `blocked` takes its tone from the fold, so a reason the backend calls a
// warning is a warning and one it merely states is not.
//
// THE ACTION IS THE BACKEND'S, and its addressee is the pane the card was drawn
// for. `refresh` exists only where the wire said `refresh`, `host-settings` only
// where it said `host-settings` AND the tab has a saved connection to edit; a
// reason that offers no remedy (`install-failed`, `helper-lost` with `none`)
// draws no button at all, and so does an action a surface did not hand this
// component. A press is dropped when the pane in front is no longer that pane,
// because the axis this button belongs to has left the screen (ALX-1019).

import { Show } from 'solid-js'
import { t } from '../i18n/locale-model'
import type { PaneConnection } from '../generated/pane.connection'
import { Button } from '../ui/button'
import { StatusCard, type StatusCardTone } from '../ui/status-card'
import { helperAxis, savedProfileId, withSavedConnection, type HelperAxis } from './helper-axis'

interface HelperAxisCardProps {
  /** The surface's identity in test ids: 'git' draws `git-helper-axis`,
   *  `git-helper-refresh`; 'files' draws the same names under `files-`. */
  surface: string
  /** What this surface needs the helper for — the card's headline, in the
   *  surface's own words ("Git needs …", "Files needs …"). */
  title: string
  /** The heading and the body of the WAITING card (`starting`, and an
   *  observation that has not arrived): what this surface will do once the
   *  helper serves. The surface's own words, because Git waits to read a
   *  repository and Files waits to show a tree. */
  startingTitle: string
  startingDescription: string
  /** The connection's helper member, verbatim from the observation. `null`
   *  or `undefined` is "nothing told yet": the card draws the waiting state
   *  the gate already put this tab in. */
  helper: PaneConnection['helper'] | null | undefined
  /** The backend pane this card was drawn for — the action's addressee. */
  paneId: string | null
  /** The pane in front AT THE PRESS, read live: the authority on whether the
   *  action is still about something on screen. */
  livePaneId: () => string | null
  /** The saved connection this tab was opened from, as `savedProfileId`
   *  answers it. */
  profileId: string | null | undefined
  /** The one action the fold may draw. Absent means no button — a surface
   *  handed no seam promises nothing it cannot keep. */
  onRefresh?: (paneId: string) => void
  onHostSettings?: (profileId: string) => void
}

/** The fold's tone in the kit's vocabulary. `muted` is the fold saying "this
 *  is a state, not a fault": a neutral card, never a warning invented here. */
function cardTone(tone: HelperAxis['tone']): StatusCardTone {
  switch (tone) {
    case 'danger':
      return 'danger'
    case 'warning':
      return 'warning'
    case 'muted':
      return 'neutral'
  }
}

export function HelperAxisCard(props: HelperAxisCardProps) {
  /** The fold's answer. Null is "nothing told, nothing claimed": the card
   *  then draws the WAITING state rather than nothing at all — the gate
   *  already tells the panel to ask nothing, and an empty panel would leave
   *  the person looking at a blank where the reason goes. */
  const axis = () => withSavedConnection(helperAxis(props.helper ?? null), props.profileId)

  /** Whether the card is the neutral waiting one: `starting`, and the
   *  observation that has not arrived (which `helperGate` answers the same
   *  way). `serving` is not a card at all — `sentence === null` is exactly
   *  that state, and the surface shows its content instead. */
  const waiting = (): boolean => {
    const facts = axis()
    return facts === null || facts.state === 'starting'
  }

  const visible = (): boolean => waiting() || axis()?.sentence !== null

  /** The one action the backend offered, or nothing. Reads the LIVE pane at
   *  press time rather than the one captured at paint. */
  const action = () => {
    const facts = axis()
    if (facts === null) return undefined
    if (facts.action === 'refresh') {
      const pane = props.paneId
      const press = props.onRefresh
      if (pane === null || press === undefined) return undefined
      return (
        <Button
          size="sm"
          data-testid={`${props.surface}-helper-refresh`}
          onClick={() => {
            if (props.livePaneId() !== pane) return
            press(pane)
          }}
        >
          {t('helper.refresh', 'Refresh')}
        </Button>
      )
    }
    if (facts.action === 'host-settings') {
      const saved = savedProfileId(props.profileId)
      const press = props.onHostSettings
      if (saved === null || press === undefined) return undefined
      return (
        <Button
          size="sm"
          data-testid={`${props.surface}-host-settings`}
          onClick={() => press(saved)}
        >
          {t('helper.hostSettings', 'Host settings')}
        </Button>
      )
    }
    return undefined
  }

  return (
    <Show when={visible()}>
      <div
        data-testid={`${props.surface}-helper-axis`}
        data-helper={waiting() ? 'starting' : (axis()?.state ?? 'starting')}
      >
        <StatusCard
          tone={waiting() ? 'neutral' : cardTone(axis()?.tone ?? 'muted')}
          title={waiting() ? props.startingTitle : props.title}
          description={waiting() ? props.startingDescription : (axis()?.sentence ?? undefined)}
          action={waiting() ? undefined : action()}
        />
      </div>
    </Show>
  )
}
