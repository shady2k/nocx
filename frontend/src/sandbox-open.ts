/**
 * sandbox-open — the Quick Connect "Sandboxed opencode…" flow (ADR-0034 §4.2).
 *
 * Extracted from the composition root so the one flow that turns a picker and
 * a permission dialog into a new tab is testable without booting `main()`. The
 * backend is the sole policy author: this reads one fresh settings snapshot,
 * shows the permission dialog seeded from the persisted baseline, and forwards
 * only the canonical workspace plus the confirmed deltas — never a baseline or
 * an effective policy root.
 */
import type { SandboxLaunch } from './ipc'
import type {
  SandboxPermissionsOptions,
  SandboxPermissionsResult,
} from './sandbox-permissions-dialog'

export interface SandboxOpenFlowDeps {
  /** One fresh settings snapshot (revision + values), read after the action. */
  getSnapshot(): Promise<{ values: Record<string, unknown>; revision: number }>
  /** The native folder picker for the workspace and for ephemeral additions. */
  openDirectory(): Promise<{ path: string }>
  /** The permission dialog. Resolves to deltas, or null when cancelled. */
  showPermissions(options: SandboxPermissionsOptions): Promise<SandboxPermissionsResult | null>
  /** Opens the sandboxed tab. Never called when the flow was cancelled. */
  newSandboxedTab(workspace: string, launch: SandboxLaunch): void
  /** Typed-failure toast (picker or snapshot failure). */
  reportError(message: string): void
}

/** The settings key carrying the persisted writable baseline. Named here,
 *  deliberately — a consumer of a specific setting has to name what it
 *  consumes (the same rule that names PLACEMENT_KEY/THEME_KEY in main.tsx). */
export const SANDBOX_PATHS_KEY = 'sandbox.allowedWritablePaths'

/**
 * Run the sandboxed-opencode open flow. Any picker or dialog cancellation
 * creates no tab; a thrown failure is reported through `reportError`.
 */
export async function openSandboxedOpenCode(deps: SandboxOpenFlowDeps): Promise<void> {
  try {
    const snap = await deps.getSnapshot()
    const raw = snap.values[SANDBOX_PATHS_KEY]
    const baseline = Array.isArray(raw) ? raw.filter((x): x is string => typeof x === 'string') : []

    const workspace = await deps.openDirectory()
    if (!workspace.path) return

    const result = await deps.showPermissions({
      workspace: workspace.path,
      baseline,
      openDirectory: () => deps.openDirectory(),
    })
    if (!result) return

    deps.newSandboxedTab(workspace.path, {
      settingsRevision: snap.revision,
      add: result.add,
      remove: result.remove,
    })
  } catch (err) {
    deps.reportError(err instanceof Error ? err.message : String(err))
  }
}
