import type { Dispatcher } from './dispatcher'
import type { SkillsApprove } from './generated/skills.approve'
import type { SkillsList } from './generated/skills.list'
import type { SkillsSetEnabled } from './generated/skills.setEnabled'
import type { SkillsRemove } from './generated/skills.remove'

export class SkillsClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  list(): Promise<SkillsList> {
    return this.dispatcher.call<SkillsList>('skills.list', {})
  }

  setEnabled(name: string, enabled: boolean): Promise<SkillsSetEnabled> {
    return this.dispatcher.call<SkillsSetEnabled>('skills.setEnabled', { name, enabled })
  }

  remove(name: string): Promise<SkillsRemove> {
    return this.dispatcher.call<SkillsRemove>('skills.remove', { name })
  }

  approve(name: string): Promise<SkillsApprove> {
    return this.dispatcher.call<SkillsApprove>('skills.approve', { name })
  }
}
