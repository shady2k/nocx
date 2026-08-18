/**
 * sandbox-open — the Quick Connect "Sandboxed shell…" flow (ADR-0037).
 *
 * Extracted from the composition root so the one flow that turns a picker and
 * a permission dialog into a new tab is testable without booting `main()`. The
 * backend is the sole policy author: this reads one fresh settings snapshot,
 * shows the permission dialog seeded from both persisted baselines, and
 * forwards only the canonical workspace plus the confirmed class-scoped deltas
 * — never a baseline or an effective policy root.
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

/** The settings key carrying the persisted read-write baseline. Named here,
 *  deliberately — a consumer of a specific setting has to name what it
 *  consumes (the same rule that names PLACEMENT_KEY/THEME_KEY in main.tsx). */
export const SANDBOX_WRITABLE_PATHS_KEY = 'sandbox.allowedWritablePaths'

/** The settings key carrying the persisted read-only baseline (ADR-0036 §3.1). */
export const SANDBOX_READ_ONLY_PATHS_KEY = 'sandbox.allowedReadOnlyPaths'

/**
 * Run the sandboxed-shell open flow. Any picker or dialog cancellation
 * creates no tab; a thrown failure is reported through `reportError`.
 */
export async function openSandboxedShell(deps: SandboxOpenFlowDeps): Promise<void> {
  try {
    const snap = await deps.getSnapshot()
    const rawWritable = snap.values[SANDBOX_WRITABLE_PATHS_KEY]
    const rawReadOnly = snap.values[SANDBOX_READ_ONLY_PATHS_KEY]
    const baselineWritable = Array.isArray(rawWritable)
      ? rawWritable.filter((x): x is string => typeof x === 'string')
      : []
    const baselineReadOnly = Array.isArray(rawReadOnly)
      ? rawReadOnly.filter((x): x is string => typeof x === 'string')
      : []

    const workspace = await deps.openDirectory()
    if (!workspace.path) return

    const result = await deps.showPermissions({
      workspace: workspace.path,
      baselineWritable,
      baselineReadOnly,
      openDirectory: () => deps.openDirectory(),
    })
    if (!result) return

    deps.newSandboxedTab(workspace.path, {
      settingsRevision: snap.revision,
      addWritable: result.addWritable,
      removeWritable: result.removeWritable,
      addReadOnly: result.addReadOnly,
      removeReadOnly: result.removeReadOnly,
    })
  } catch (err) {
    deps.reportError(err instanceof Error ? err.message : String(err))
  }
}
