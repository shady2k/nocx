// WHAT A PANE IS DOING, and how strong the evidence is (nocx-szb40.3,
// nocx-szb40.4).
//
// There are two sources and there may never be a third answer that merges
// them badly, so this module is the single owner of the merge (AD-8).
//
// THE DRIVER is the strong one: the backend keeps a live VT grid for a pane
// somebody enrolled, an agent-specific driver classifies its chrome, and the
// result crosses as session.observationChanged. It knows the difference
// between a turn in flight, a dialog waiting on a human, and an input box
// waiting for a prompt.
//
// THE TITLE is the weak one, and it stays. `detectAgentStatus` reads OSC 0/2
// and answers working or idle for any pane at all — including every pane
// nobody enrolled, which is almost all of them. Its own comment says what it
// is worth: BRAILLE_SPINNER matches any braille glyph in any title, so
// `npm install` under ora reads as an agent working. That is why it is
// labelled rather than trusted, and why a driver's answer displaces it
// entirely for the panes that have one.
//
// The source travels WITH the value, for the same reason the notification
// design gives about trust classes: a surface that shows a guess and a
// finding identically teaches people to read findings as guesses.

import type { AgentStatus } from './agent-status'
import type { SessionObservationChanged } from './generated/session.observationChanged'

/** The closed set the backend classifies into. Re-exported from the generated
 *  contract type rather than restated, so a member added to the schema cannot
 *  be missing here.
 *
 *  Named for the DRIVER rather than for the pane, because the overview already
 *  owns a `PaneState` and it answers a different question — what a person has
 *  to do about a pane, which is a projection of this one and not a synonym. */
export type DriverState = SessionObservationChanged['state']

const PANE_STATES: readonly DriverState[] = [
  'free_text',
  'permission_choice',
  'modal_choice',
  'working',
  'error',
  'unknown',
  'exited',
]

/** The boundary guard. A value nobody wrote a branch for must not reach the
 *  indicator: every consumer treats what it cannot read as busy, and that only
 *  holds if what it cannot read never gets through. */
export function isDriverState(v: unknown): v is DriverState {
  return typeof v === 'string' && (PANE_STATES as readonly string[]).includes(v)
}

/** What every surface shows for a pane: the tab's dot, the overview card, the
 *  workspace chip's attention mark.
 *
 *  It EXTENDS AgentStatus rather than standing beside it. A second vocabulary
 *  for one concept is the defect two epics spent themselves unwinding, and the
 *  two would disagree at exactly the moment it mattered — a pane whose title
 *  still spins while its driver says a dialog is waiting.
 *
 *  Deliberately smaller than DriverState, but NOT everywhere: the two kinds of
 *  menu need a person identically, and 'error' needs one differently from both
 *  of its neighbours. WHICH menu it is decides whether answering it answers the
 *  agent, and that matters to the caller that TYPES (nocx-dkawo.1), never to a
 *  dot on a tab. */
export type PaneActivity = AgentStatus | 'waiting' | 'error' | 'unknown' | 'exited'

/** Where the value came from. `title` is the weaker row of the provenance
 *  table and may light the indicator; it decides nothing. */
export type PaneActivitySource = 'driver' | 'title'

export interface PaneIndicator {
  activity: PaneActivity
  source: PaneActivitySource
}

/** The driver's closed set, projected onto what a tab can show. */
function fromDriver(state: DriverState): PaneActivity {
  switch (state) {
    case 'free_text':
      return 'idle'
    case 'working':
      return 'working'
    case 'permission_choice':
    case 'modal_choice':
      return 'waiting'
    // 'error' does NOT collapse, and this is the one projection that must not.
    // The two menus collapse because a dot cannot act on the difference; error
    // is the opposite case — folding it into 'working' says leave it alone,
    // and folding it into 'waiting' says answer something that is not there.
    case 'error':
      return 'error'
    case 'exited':
      return 'exited'
    case 'unknown':
      return 'unknown'
  }
}

/**
 * Merge the two sources into what the tab shows.
 *
 * A DRIVER OBSERVATION DISPLACES THE TITLE ENTIRELY, including when it says
 * `unknown`. That is the point of having it: the title says "working" for a
 * pane whose agent is blocked on a dialog, because the spinner is still in the
 * title — and preferring the title there would show a busy worker that is
 * actually waiting for a person. "The driver cannot read this screen" is a
 * better answer than a confident wrong one.
 *
 * Null means the pane has nothing to say: no driver, and a title that never
 * mentioned an agent. The classifier's own comment is the reason it is not
 * `idle` — a title that never carried a spinner is not an idle agent.
 */
export function paneIndicator(
  observation: DriverState | null,
  titleStatus: AgentStatus | null,
  reachable: boolean = true,
): PaneIndicator | null {
  // AND AN UNREACHABLE HOST DISPLACES BOTH — but only down to `unknown`, and
  // only when there was something to say in the first place.
  //
  // This is not a priority rule and the difference matters. Both sources
  // describe what an agent was doing when it was last OBSERVED, and neither
  // can be observed through a host that has stopped answering: the driver's
  // classification is a reading of a screen no bytes are arriving from, and
  // the title is the last one that reached us. Reporting "waiting for you"
  // over a dead pipe calls a person to answer something that cannot be
  // delivered. So the fact stops being asserted, exactly as AD-6 refuses to
  // assert a grid's state for a pane it did not watch from byte zero.
  //
  // `unknown` rather than null, because null means "there is no agent in this
  // pane" — a meaning the tab already draws by showing no dot at all. An
  // unobservable agent is not an absent one, and losing that distinction is
  // worst at the moment it matters: scanning a strip of tabs after a suspend,
  // deciding which to rescue first, "this one has an agent in it" is the
  // deciding fact.
  const indicator = observedIndicator(observation, titleStatus)
  if (indicator === null) return null
  if (!reachable) return { activity: 'unknown', source: indicator.source }
  return indicator
}

function observedIndicator(
  observation: DriverState | null,
  titleStatus: AgentStatus | null,
): PaneIndicator | null {
  if (observation !== null) return { activity: fromDriver(observation), source: 'driver' }
  if (titleStatus !== null) return { activity: titleStatus, source: 'title' }
  return null
}
