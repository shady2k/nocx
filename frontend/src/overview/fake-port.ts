// A fake `OverviewPort`, for the tests of everything built on it.
//
// It exists so the surface's failure paths can be STATED rather than
// constructed: "no workspaces at all", "a pane that has composed no title
// yet", "a workspace the renderer drew no rows for" are three lines here and
// three sessions through the real chain. AGENTS.md testing rule 3 asks for
// every one of them; this is what makes asking cheap.
import type {
  OverviewPaneFacts,
  OverviewPort,
  OverviewSnapshot,
  OverviewWorkspaceFacts,
} from './overview-port'

/** A pane that knows nothing, so a test states only the facts it is about. */
export function fakePane(over: Partial<OverviewPaneFacts> = {}): OverviewPaneFacts {
  return {
    paneId: 'pane-1',
    title: null,
    host: null,
    cwd: { state: 'unobserved' },
    branch: null,
    agentStatus: null,
    runningCommand: null,
    failed: false,
    since: null,
    lastLine: null,
    fullScreen: false,
    lastBlock: null,
    excerpt: [],
    ...over,
  }
}

/** A workspace holding the panes given. `name: null` remains the DEFAULT
 *  marker; the overview presents that loose-pane collection as "Ungrouped". */
export function fakeWorkspace(
  id: string,
  name: string | null,
  panes: readonly OverviewPaneFacts[] = [],
): OverviewWorkspaceFacts {
  return { id, name, panes }
}

export class FakeOverviewPort implements OverviewPort {
  /** Every pane id `activate` was called with, in order. */
  readonly activated: string[] = []
  /** Every workspace id `switchWorkspace` was called with, in order. */
  readonly switched: string[] = []
  /** Every workspace id `createTab` was called with, in order. */
  readonly tabsCreated: string[] = []
  /** Every workspace id `closeWorkspace` was called with, in order. */
  readonly workspacesClosed: string[] = []
  workspaceCloseResult = true
  /** How many times the board's last column was pressed. */
  workspacesCreated = 0
  private listeners = new Set<() => void>()

  constructor(private state: OverviewSnapshot = { workspaces: [], activePaneId: null }) {}

  snapshot(): OverviewSnapshot {
    return this.state
  }

  /** How many times the overview handed the keyboard back to the front pane. */
  focusedActive = 0

  focusActive(): void {
    this.focusedActive += 1
  }

  activate(paneId: string): void {
    this.activated.push(paneId)
  }

  switchWorkspace(workspaceId: string): void {
    this.switched.push(workspaceId)
  }

  createTab(workspaceId: string): void {
    this.tabsCreated.push(workspaceId)
  }

  closeWorkspace(workspaceId: string): Promise<boolean> {
    this.workspacesClosed.push(workspaceId)
    // Resolved rather than `async`: the fake answers from a field and awaits
    // nothing, and an async function with no await is a promise of work that
    // does not happen.
    return Promise.resolve(this.workspaceCloseResult)
  }

  createWorkspace(): void {
    this.workspacesCreated += 1
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  /** Replace what the application would answer, and say so. */
  setSnapshot(next: OverviewSnapshot): void {
    this.state = next
    for (const l of [...this.listeners]) l()
  }

  /** How many listeners are still attached — what proves a close let go. */
  get listenerCount(): number {
    return this.listeners.size
  }
}
