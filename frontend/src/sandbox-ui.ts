import type { WSClient } from './ipc'
import type { SandboxOperationResult } from './generated/sandbox.replace'
import type { Workspace } from './generated/layout.read'

/** The root-owned transport, narrowed without copying its wire contracts. */
type SandboxClient = Pick<
  WSClient,
  | 'sandboxStatus'
  | 'sandboxProfile'
  | 'sandboxUpdateProfile'
  | 'sandboxResetProfile'
  | 'sandboxPreview'
  | 'sandboxReplace'
  | 'sandboxCancel'
  | 'sandboxOperation'
  | 'sandboxGrant'
  | 'sandboxAccessList'
  | 'sandboxResolveAccess'
  | 'onSandboxAccessChanged'
>

/** Capture before activating Settings; never substitute the newly active pane. */
export interface SandboxPaneContext {
  readonly paneId: string
  readonly workspaceId: string
  readonly kind: 'local' | 'ssh'
  readonly registered: Promise<boolean>
  readonly isCurrent: () => boolean
}

/** True only when the existing Settings pane has opened its Sandbox page. */
export type SandboxNavigationRequest = (context: SandboxPaneContext) => Promise<boolean>

export interface SandboxSettingsServices {
  readonly client: SandboxClient
  readonly workspaces: () => readonly Pick<Workspace, 'id' | 'name'>[]
  readonly defaultWorkspaceId: () => string
  readonly bindCandidate: (
    context: SandboxPaneContext,
    operation: SandboxOperationResult,
  ) => Promise<boolean>
  readonly statusChanged: () => void
}
