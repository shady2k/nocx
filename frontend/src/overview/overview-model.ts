// What the overview's raw facts MEAN — the whole judgement layer, in one
// module with no DOM in it (bead nocx-edhcu).
//
// It is here rather than in the surface for the reason rule 4 of AGENTS.md's
// testing section gives: a derivation buried in JSX can only be checked by
// rendering, and a test that renders asserts what the markup happens to say.
// Every sentence this surface prints is decided by a function below, and every
// one of them has a test that never mounts anything.
import type { StatusDotTone } from '../ui/status-dot'
import type { WorkspaceColour } from '../layout/workspace-colours'

import type { CommandStatus } from '../command-ledger'
import type { PaneActivity } from '../pane-observation'
import type { OverviewBlockFacts, OverviewPaneFacts, OverviewSnapshot } from './overview-port'

/**
 * What a pane is doing, in the vocabulary a person uses when they are looking
 * for the one thing that needs them.
 *
 * `waiting` is the point of the whole surface: an agent that has stopped and
 * is asking a human something. It is NOT `idle` — see `paneState`.
 */
export type PaneState = 'waiting' | 'running' | 'failed' | 'idle'

/** Worst first. A workspace wears the worst state among its panes, and a
 *  failure outranks a question: one is a thing that has gone wrong, the other
 *  is a thing waiting to go right. */
const SEVERITY: readonly PaneState[] = ['failed', 'waiting', 'running', 'idle']

/** A value the application could not answer. '' and null are the same
 *  absence — `terminal-content.ts` reports an unknown host as '' and a pane
 *  with no session as null, and neither is something to print. */
function present(value: string | null): string | null {
  const trimmed = (value ?? '').trim()
  return trimmed === '' ? null : trimmed
}

/**
 * WHICH STATE A PANE IS IN, AND THE ONE INVERSION THAT MATTERS.
 *
 * `agent-status.ts` classifies a program's own title into `'working'` (a
 * braille spinner frame — something is happening) and `'idle'` (Claude's ✳ —
 * the agent has STOPPED and is waiting on you). Its word for the second is
 * `idle` because it is describing the agent's activity; ours is `waiting`
 * because we are describing what the person has to do about it. Reading that
 * `'idle'` as our `idle` would file the one pane that needs a human under
 * "nothing to see", which is the exact failure this surface exists to prevent.
 *
 * A failure beats every other signal: a title can linger unrepainted after the
 * process it described has died.
 */
export function paneState(f: OverviewPaneFacts): PaneState {
  if (f.failed) return 'failed'
  // Everything that wants a person: an agent that went idle, one holding a
  // dialog open, and one whose process is gone with its work behind it.
  if (f.agentStatus === 'idle' || f.agentStatus === 'waiting') return 'waiting'
  if (f.agentStatus === 'exited') return 'waiting'
  // 'unknown' is the driver saying it could not read the screen, and every
  // consumer treats that as busy. Filing it under "nothing to see" would be
  // the failure this surface exists to prevent, in the other direction.
  if (f.agentStatus === 'working' || f.agentStatus === 'unknown') return 'running'
  if (present(f.runningCommand) !== null) return 'running'
  return 'idle'
}

const STATE_LABEL: Record<PaneState, string> = {
  waiting: 'Waiting on you',
  running: 'Running',
  failed: 'Failed',
  idle: 'Idle',
}

const STATE_TONE: Record<PaneState, StatusDotTone> = {
  waiting: 'warning',
  running: 'ok',
  failed: 'error',
  idle: 'neutral',
}

/** The tone the dot and the sentence beside it both wear. One table, so they
 *  can never disagree. */
export function stateTone(state: PaneState): StatusDotTone {
  return STATE_TONE[state]
}

export function stateLabel(state: PaneState): string {
  return STATE_LABEL[state]
}

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/**
 * How long ago, in the coarsest unit that is still true: `12s`, `5m`, `3h`,
 * `2d`. Null when nothing recorded when the state began — an unknown age is
 * printed as nothing rather than as zero, because "just now" is a claim.
 *
 * A `since` in the future reads as `0s` rather than as a negative duration:
 * the two clocks are allowed to disagree, and the surface is not the place to
 * argue about it.
 */
export function ageLabel(since: number | null, now: number): string | null {
  if (since === null) return null
  const elapsed = Math.max(0, now - since)
  if (elapsed < MINUTE) return `${Math.floor(elapsed / SECOND)}s`
  if (elapsed < HOUR) return `${Math.floor(elapsed / MINUTE)}m`
  if (elapsed < DAY) return `${Math.floor(elapsed / HOUR)}h`
  return `${Math.floor(elapsed / DAY)}d`
}

/**
 * The state and its age as one sentence — what the card's status line says.
 *
 * A live state reads as a duration ("Waiting on you for 5m") and a dead one as
 * an elapsed time ("Failed 2m ago"), because they are different questions: one
 * asks how long this has been true, the other how long ago it happened.
 */
export function stateText(f: OverviewPaneFacts, now: number): string {
  const state = paneState(f)
  const label = STATE_LABEL[state]
  const age = ageLabel(f.since, now)
  if (age === null) return label
  return state === 'failed' ? `${label} ${age} ago` : `${label} for ${age}`
}

/**
 * What the card calls the pane: the title it composed for itself, and — when
 * it has composed none yet — the same chain one rung at a time.
 *
 * The last resort is a name rather than an empty string. A blank card is
 * indistinguishable from a rendering bug, and a pane one round trip old is an
 * ordinary state, not a broken one.
 */
export function cardTitle(f: OverviewPaneFacts): string {
  return (
    present(f.title) ??
    present(f.runningCommand) ??
    present(f.cwd) ??
    present(f.host) ??
    'Untitled pane'
  )
}

/**
 * Where the pane is: the machine, the directory and the branch, in that order,
 * and only the parts that are known.
 *
 * A part equal to the title is dropped. A pane sitting at a prompt is titled
 * by its cwd (`pushTitle`: program title, else running command, else cwd), so
 * printing that cwd underneath would be one fact twice — the same reason
 * `terminal-content.ts` suppresses its own location line.
 */
export function cardLocation(f: OverviewPaneFacts): string | null {
  const title = present(f.title)
  const parts = [present(f.host), present(f.cwd), present(f.branch)].filter(
    (p): p is string => p !== null && p !== title,
  )
  return parts.length === 0 ? null : parts.join(' · ')
}

/**
 * WHAT THE CARD QUOTES UNDERNEATH THE STATUS — one line, and which line it is
 * depends on what the pane is doing, because one rule cannot serve all four.
 *
 * The first version quoted the last line the pane drew, always. It is right
 * for exactly one of the states below and actively misleading in the other
 * three: a `top` was quoted one row of its process table, and an agent
 * waiting on a human was quoted its own empty prompt (`❯`). Both are the
 * bottom row of the buffer, and neither is anything the pane said.
 *
 * In order:
 *
 * 1. AN AGENT gets no quote at all. The title is the agent's own name and the
 *    status line says whether it is working or waiting on you — that is the
 *    whole answer, and a line of its frame underneath only competes with it.
 * 2. A FULL-SCREEN PROGRAM is named, not quoted. It owns the alternate buffer,
 *    so it has produced no blocks and its bottom row is part of a picture it
 *    repaints.
 * 3. OTHERWISE THE LAST BLOCK: the command the pane last ran and how it ended.
 *    "What did this pane last do" is answered by a command and an outcome.
 * 4. AND WHEN THERE IS NO BLOCK — a session with no shell integration never
 *    produces one — the last drawn line, which is the best that can be had.
 */
/**
 * The facts a quote is derived from — the SUBSET of a pane's facts that
 * decides what it says about itself.
 *
 * Narrower than OverviewPaneFacts on purpose, and it is what lets the tab
 * strip's rows say the same sentence as the overview's cards without either
 * of them growing a second derivation: the strip holds a pane, not a
 * snapshot, and asking it to fabricate a paneId, a host and a branch to get a
 * string would be paying for the wrong shape. `OverviewPaneFacts` satisfies
 * this structurally, so the overview passes its own facts unchanged.
 */
export interface QuoteFacts {
  readonly title: string | null
  readonly agentStatus: PaneActivity | null
  readonly runningCommand: string | null
  readonly fullScreen: boolean
  readonly lastBlock: OverviewBlockFacts | null
  readonly lastLine: string | null
}

export function cardQuote(f: QuoteFacts): string | null {
  // AN AGENT IS QUOTED ONLY WHEN IT HAS SAID SOMETHING. Its last line is
  // often the most useful thing on the surface — an agent that has stopped is
  // usually stopped ON A QUESTION, and that question is the whole reason a
  // person opened the overview. What it must not be is the agent's own empty
  // prompt: Claude Code draws an input box, so a pane waiting on a human was
  // quoted "❯" and told the reader nothing at all. So the furniture is
  // dropped and the words are kept.
  if (f.agentStatus !== null) {
    const line = present(f.lastLine)
    return line === null || isPromptFurniture(line) ? null : line
  }
  if (f.fullScreen) {
    const name = present(f.runningCommand) ?? present(f.title)
    return name === null ? 'Full screen' : `${name} · full screen`
  }
  const block = f.lastBlock
  if (block !== null && present(block.command) !== null) {
    const outcome = blockOutcome(block.status, block.exitCode)
    return outcome === null ? block.command.trim() : `${block.command.trim()} · ${outcome}`
  }
  return present(f.lastLine)
}

/**
 * How a block ended, in the fewest words that are still exact — or null when
 * saying anything would be a claim.
 *
 * A running block gets nothing: the status line above it already says
 * "Running for 3m", and repeating it here would spend the only line the card
 * has on a fact it already carries. `unknown` is a real answer from a shell
 * that reported no status, and it is printed as silence rather than as a
 * guess at success.
 */
/** A line that is a prompt and nothing else — one or two glyphs of shell or
 *  agent furniture. Quoting it says only "this pane is waiting", which the
 *  status line above has already said. */
function isPromptFurniture(line: string): boolean {
  return /^[>❯›»$#%λ⯈▶]{1,2}$/u.test(line)
}

function blockOutcome(status: CommandStatus, exitCode: number | null): string | null {
  switch (status) {
    case 'success':
      return 'ok'
    case 'failure':
      return exitCode === null ? 'failed' : `exit ${exitCode}`
    case 'interrupted':
      return 'interrupted'
    case 'running':
    case 'unknown':
      return null
  }
}

/** The worst state among a workspace's panes; `idle` when it has none. */
export function workspaceAttention(states: readonly PaneState[]): PaneState {
  for (const candidate of SEVERITY) {
    if (states.includes(candidate)) return candidate
  }
  return 'idle'
}

/** One pane as the surface draws it — every string already decided. */
export interface OverviewCard {
  readonly paneId: string
  readonly title: string
  readonly location: string | null
  readonly state: PaneState
  readonly stateText: string
  /** The one line under the status — see cardQuote. Null when the card has
   *  nothing to add that its title and status do not already say. */
  readonly quote: string | null
  /** A few lines of what the pane is showing, verbatim — the card's picture.
   *  Empty for a pane with nothing to show. */
  readonly excerpt: readonly string[]
  readonly isActive: boolean
}

/** One area of the overview: a named workspace, or the ungrouped panes. */
export interface OverviewGroup {
  readonly id: string
  /** Null is retained as the model marker; the surface labels it Ungrouped. */
  readonly name: string | null
  readonly colour: WorkspaceColour | null
  readonly isDefault: boolean
  readonly attention: PaneState
  readonly cards: readonly OverviewCard[]
}

/**
 * The snapshot, resolved into what gets drawn.
 *
 * THE UNGROUPED AREA GOES FIRST, and it was last for one round. The argument
 * for last was §4.3's sketch of the vertical strip, which draws the default's
 * rows below the named workspaces. Two things were wrong with reading it that
 * way. The horizontal strip — the one a person actually looks at — draws them
 * FIRST, because the chain's order puts them there and nothing sweeps them
 * elsewhere; so last here made the two surfaces disagree about the order of
 * the same objects, which is the shape AD-8 is about. And it buried the
 * common case: a person who has never made a workspace has every pane they
 * own in the default, and this surface was showing them last, under every
 * container they had made since.
 */
export function overviewGroups(snapshot: OverviewSnapshot, now = Date.now()): OverviewGroup[] {
  const groups = snapshot.workspaces.map((w): OverviewGroup => {
    const cards = w.panes.map((f): OverviewCard => ({
      paneId: f.paneId,
      title: cardTitle(f),
      location: cardLocation(f),
      state: paneState(f),
      stateText: stateText(f, now),
      quote: cardQuote(f),
      excerpt: f.excerpt,
      isActive: f.paneId === snapshot.activePaneId,
    }))
    return {
      id: w.id,
      name: present(w.name),
      colour: w.colour ?? null,
      isDefault: present(w.name) === null,
      attention: workspaceAttention(cards.map((c) => c.state)),
      cards,
    }
  })
  return [...groups.filter((g) => g.isDefault), ...groups.filter((g) => !g.isDefault)]
}

/** "3 panes" / "1 pane" — the count the strip shows beside a workspace. */
export function paneCountLabel(count: number): string {
  return count === 1 ? '1 pane' : `${count} panes`
}
