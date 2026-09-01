// The overview's PORT: everything this surface needs from the application,
// and nothing else (bead nocx-edhcu).
//
// WHY A PORT AND NOT A REACH INTO PaneManager. Interface-first + DI is the
// repo's rule (AGENTS.md, "Engineering rules"), and here it also buys the one
// thing the surface cannot get any other way: a fake. Every failure path this
// surface has — no workspaces at all, a pane that has told us nothing yet, a
// workspace whose rows the renderer never drew — is a state that is expensive
// to reach through the real chain and free to state here.
//
// WHAT IS DELIBERATELY NOT IN IT. No drag, no move, no close, no rename: the
// overview reads the application and expresses exactly ONE intent, `activate`.
// Dragging a card between workspaces is filed separately (nocx-edhcu child)
// because a move is a write against a lifecycle the backend owns (§4.4), and a
// read-only surface is the honest first half.
//
// THE FACTS ARE RAW, THE JUDGEMENT IS OURS. The port carries what the
// application already knows — the composed title, the machine, the cwd, the
// agent's own signal, the foreground command — and `overview-model.ts` turns
// them into a state and a sentence. The alternative, a port that hands over a
// finished `state`, would put the ✳-means-waiting rule in the composition root
// where nothing tests it.
import type { PaneActivity } from '../pane-observation'
import type { CommandStatus } from '../command-ledger'
import type { WorkspaceColour } from '../layout/workspace-colours'

/** The helper's three answers about a session's current working directory.
 *
 * The launch directory is deliberately not carried here. It is a historical
 * fact, not an observation, and using it as a fallback would make a stale
 * path look current.
 */
export type CwdObservation =
  | { readonly state: 'unobserved' }
  | { readonly state: 'known'; readonly cwd: string }
  | { readonly state: 'unavailable' }

/** One pane, as the application knows it at the moment the overview opens. */
export interface OverviewPaneFacts {
  /** The pane's durable id — `Pane.wireId`, the id `activate` takes back. */
  readonly paneId: string
  /**
   * The title the pane composed for itself: the program's own title, else the
   * running command, else the cwd (terminal-content.ts `pushTitle`; tabs and
   * panes design §4.5). Null for a pane one round trip old that has composed
   * nothing yet — which is a real state and not an error.
   */
  readonly title: string | null
  /** The machine this pane is talking to (`user@host` or `host`), or null for
   *  a local session and for a pane holding no session at all. */
  readonly host: string | null
  /** The shell's current directory as observed by the helper or local shell. */
  readonly cwd: CwdObservation
  /** The git branch the pane's repository is on, or null when the pane is not
   *  in a repository, or when nothing has asked. */
  readonly branch: string | null
  /**
   * What the PROGRAM'S OWN title says about an agent running in the pane, as
   * `agent-status.ts` classifies it. Null means the title carries no agent
   * signal at all — a plain shell — and null is NOT idle.
   *
   * Read the vocabulary carefully: `'working'` is a spinner frame, and
   * `'idle'` is Claude's ✳, which means the agent has stopped and is WAITING
   * ON A HUMAN. The model turns that into `'waiting'`, which is the whole
   * question this surface answers.
   */
  readonly agentStatus: PaneActivity | null
  /** The command running in the foreground right now, or null
   *  (`PaneContent.liveWork`). */
  readonly runningCommand: string | null
  /** Whether the pane's work ended badly — a shell that exited non-zero, a
   *  connection that dropped. */
  readonly failed: boolean
  /** When the pane entered the state it is in, epoch milliseconds, or null
   *  when nothing recorded it. Null is honest; a made-up `Date.now()` at read
   *  time would report every pane as one second old. */
  readonly since: number | null
  /** The last line of output the renderer drew, or null when it drew none. */
  readonly lastLine: string | null
  /**
   * Whether a program is drawing on the ALTERNATE buffer — a `top`, a `vim`,
   * a pager — and therefore owns the whole screen.
   *
   * It is carried as the raw fact rather than as a finished sentence because
   * it changes what the card can honestly say: a full-screen program produces
   * no blocks, and its bottom row is part of a repainted picture rather than
   * something the pane said. Quoting one row of `top`'s process table is how
   * this was found.
   */
  readonly fullScreen: boolean
  /**
   * The last command the pane ran — finished or still running — or null for a
   * pane that has run none, and for a session with no shell integration,
   * where blocks do not exist at all.
   */
  readonly lastBlock: OverviewBlockFacts | null
  /**
   * A few lines of what the pane is currently showing, oldest first, blanks
   * already dropped (`TerminalContent.excerpt`). Empty for a pane that has
   * drawn nothing.
   *
   * This is the card's picture, and it is TEXT because a terminal is text: a
   * pixel thumbnail of an 80-column pane at a card's width gives under four
   * pixels a character, which distinguishes "a wall of output" from "empty"
   * and nothing else. Three legible lines say which wall it is.
   */
  readonly excerpt: readonly string[]
}

/** One command block, as the application knows it (`CommandLedger`). */
export interface OverviewBlockFacts {
  readonly command: string
  readonly status: CommandStatus
  /** The shell's exit status, or null while it is running and for a shell
   *  that never reported one. */
  readonly exitCode: number | null
}

/** One workspace and the panes in it. */
export interface OverviewWorkspaceFacts {
  readonly id: string
  /**
   * The name the user gave it, or NULL for the default workspace. The overview
   * may label that area "Ungrouped", but that descriptive label is not stored
   * as the default workspace's name.
   */
  readonly name: string | null
  /** Optional only for narrow test ports; the application always supplies it. */
  readonly colour?: WorkspaceColour | null
  readonly panes: readonly OverviewPaneFacts[]
}

/** ONE reading of the application, taken when the overview opens. */
export interface OverviewSnapshot {
  /** Every workspace, in the order the switcher shows them. */
  readonly workspaces: readonly OverviewWorkspaceFacts[]
  /** The pane in front right now, so the overview can open on it. Null when
   *  no pane is active. */
  readonly activePaneId: string | null
}

/** What the overview reads and the intents it sends back to the application. */
export interface OverviewPort {
  /** Everything on screen right now. Called on open, and again whenever the
   *  application says something changed. */
  snapshot(): OverviewSnapshot
  /**
   * Bring this pane to the front — the window follows it into its workspace
   * if that is somewhere else, which `PaneManager.activate` already does.
   *
   * The overview closes itself after calling this; the port never closes it.
   */
  activate(paneId: string): void
  /**
   * Give the keyboard back to the pane that is in front.
   *
   * Called when the overview closes, whatever closed it. It is NOT "restore
   * focus to whatever opened me", and the difference is the whole point: the
   * overview covers the entire workspace, so what it hands input back to is
   * the pane in front — which is frequently NOT the pane it was opened from,
   * because choosing a card activates another pane and then closes.
   *
   * Restoring the invoker was wrong in both directions. Opened from the
   * toolbar button, it left the keyboard on a button that swallows every
   * keystroke, so typing did nothing at all. Opened and then used to choose a
   * card, it took the keyboard away from the pane the person had just picked.
   */
  focusActive(): void
  /**
   * Go to a workspace, landing on the pane the person was last in there —
   * which is the MRU question, and PaneManager already owns the answer
   * (`switchWorkspace`). The overview asks and never picks: a surface that
   * chose "the first card" would be a second answer to a question that has
   * an owner.
   *
   * The overview closes itself after calling this.
   */
  switchWorkspace(workspaceId: string): void
  /**
   * Open a new tab in the workspace named by a column's dashed footer action.
   *
   * It takes the workspace explicitly rather than opening "wherever the
   * window is": this surface shows every workspace at once, so a create with
   * no subject would land somewhere the person was not pointing at.
   */
  createTab(workspaceId: string): void
  /**
   * Close one named workspace and all of its tabs through PaneManager's
   * confirmation path. The default workspace is refused by that owner.
   */
  closeWorkspace(workspaceId: string): Promise<boolean>
  /** Make a workspace from the board-wide action below the columns. The
   *  application owns the name-and-colour dialog, not this surface. */
  createWorkspace(): void
  /**
   * Optional: tell the overview when the snapshot would answer differently,
   * so a card's state and age stay true while it is open. Returns an
   * unsubscribe. A port that does not implement it gets a surface that is
   * accurate at the moment it opened, which is the honest degrade.
   */
  subscribe?(listener: () => void): () => void
}
