/** Per-agent model-role choices over the control plane. */
import { notifyAiConfigChanged } from './ai-config'
import { Dispatcher } from './dispatcher'
import type { RolesAssignResult, Role as AssignRole } from './generated/roles.assign'
import type { RolesListResult, Role as ListRole } from './generated/roles.list'

export type WireRole = ListRole | AssignRole

export interface RoleAssignInput {
  role: string
  agentId: string
  model: string | null
}

export class RolesClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  listRoles(agentId: string): Promise<RolesListResult> {
    return this.dispatcher.call<RolesListResult>('roles.list', { agentId })
  }

  assignRole(input: RoleAssignInput): Promise<RolesAssignResult> {
    return this.dispatcher.call<RolesAssignResult>('roles.assign', input).then((value) => {
      notifyAiConfigChanged()
      return value
    })
  }
}
