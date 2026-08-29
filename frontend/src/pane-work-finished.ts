// WHEN A PANE'S WORK LOOKS FINISHED — the settle window between the
// classifier's verdict and a notification (nocx-n3nfg, notification design
// §3.4).
//
// `detectAgentStatus` (agent-status.ts) is a STATELESS classifier: it reads
// one title and answers 'working', 'idle' or null. That is the right shape
// for what it does and the wrong shape for what a notification needs, and
// §3.4 says so in as many words — it calls this "new work, not an existing
// signal". Three rules, and this module is all three:
//
// 1. ONLY THE working → idle EDGE. Never null → idle. The classifier's own
//    comment is the argument: "a title that never mentions an agent is not
//    an idle agent". A pane whose title has never carried a spinner has not
//    been working, so nothing about it has finished — and before this file
//    existed the caller did exactly that, marking activity whenever the new
//    value was idle regardless of the old one.
//
// 2. IDLE HELD FOR FIVE SECONDS. Claude Code's title oscillates ✳ ↔ spinner
//    every one to three seconds between tool calls, so a bare edge fires on
//    each of them. The window collapses a run of those into the one that
//    matters, and it is cancelled by every way the pane can stop being a
//    settled idle pane: picking the work back up, dropping its title, the
//    tab closing, and its session being replaced under it.
//
// 3. NAMED HONESTLY. This module never says "the agent finished", and
//    neither does the notification it produces. BRAILLE_SPINNER matches any
//    braille glyph in any title — `npm install` under ora, `docker pull`,
//    half of all TUIs — so the event is an inference about a pane. Its trust
//    class is `heuristic` for exactly that, and heuristic is what confines
//    it to local attention and keeps it off push (design §3.1). The words
//    are stamped by the backend; the honesty here is that this file arms one
//    timer and asserts nothing else.
//
// WHAT IS DELIBERATELY NOT HERE. Suppression — whether the person is looking
// at this very tab — and the debounce. Both belong to the router, which
// already owns them for every other source (internal/notify/policy.go), and
// a second answer here would be a second owner of "where does this go".
// This module reports; it never decides a destination.

import type { AgentStatus } from './agent-status'

/**
 * How long a pane must hold idle before its work counts as finished.
 *
 * Five seconds, and it is termic's number rather than one invented here —
 * design §3.4 rule 2 adopts it with their reasoning. Exported so a test
 * states the interval it asserts against instead of restating the digits.
 */
export const PANE_WORK_FINISHED_SETTLE_MS = 5000

export interface WorkFinishedWatchDeps {
  /**
   * The session this pane is on RIGHT NOW, or null between sessions.
   *
   * Read twice: when the window is armed, to know what would be reported,
   * and when it closes, to know it is still the same thing. A session id is
   * server-authoritative and a reattach mints a new one (AD-7), so a
   * different answer at fire time IS the session-replacement cancel — the
   * pane is told about titles, never about reattaches, so there is no event
   * to hang that cancel on and a captured id is what closes it.
   */
  session: () => string | null

  /**
   * Called at most once per settled run of work, with the session that went
   * idle. Fire-and-forget from the caller's side: this module has no opinion
   * about what happens next and no way to retry.
   */
  onFinished: (sessionId: string) => void

  /** Defaults to `PANE_WORK_FINISHED_SETTLE_MS`. */
  settleMs?: number
}

/**
 * One pane's settle window. Feed it every classifier transition the pane
 * makes; it holds a timer and nothing else.
 *
 * It deliberately does NOT hold the pane's current status. The Pane already
 * has that field and shows it in the strip, and a second copy here would be
 * one fact with two owners — so the transition is passed in rather than
 * derived, and this class only ever asks "was that the edge".
 */
export class WorkFinishedWatch {
  private timer: ReturnType<typeof setTimeout> | null = null
  /** The session the open window was armed on, or null when none is open. */
  private armedFor: string | null = null

  constructor(private readonly deps: WorkFinishedWatchDeps) {}

  /**
   * Report one classifier transition.
   *
   * Every transition cancels first, and only working → idle re-arms. That
   * single line is two of the four cancels — idle → working and the title
   * going null are both "not the edge" — and writing them as one rule is
   * what stops a later transition being forgotten: anything that is not a
   * pane settling is a pane that has not settled.
   */
  edge(prev: AgentStatus | null, next: AgentStatus | null): void {
    this.cancel()
    if (prev !== 'working' || next !== 'idle') return
    const sessionId = this.deps.session()
    // Nothing to address. sessionId is ADDRESSING (ADR-0047 §2.2), so a
    // pane between sessions has no record to make — and arming anyway would
    // let a session that arrives during the window inherit a settle it was
    // never part of.
    if (!sessionId) return
    this.armedFor = sessionId
    this.timer = setTimeout(() => {
      this.settled()
    }, this.deps.settleMs ?? PANE_WORK_FINISHED_SETTLE_MS)
  }

  /**
   * Drop any open window: the tab is closing, or the pane is going away.
   * Idempotent, and safe on a watch that never armed.
   */
  cancel(): void {
    if (this.timer !== null) clearTimeout(this.timer)
    this.timer = null
    this.armedFor = null
  }

  /** The window closed with the pane still idle. */
  private settled(): void {
    const armed = this.armedFor
    this.timer = null
    this.armedFor = null
    if (armed === null) return
    // The session-replacement cancel, checked here because this is the only
    // moment the answer can have changed. Reporting `armed` and not the
    // current session is the point: the watch speaks for the run of work it
    // watched, or it says nothing.
    if (this.deps.session() !== armed) return
    this.deps.onFinished(armed)
  }
}
