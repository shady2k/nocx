import type { Dispatcher } from './dispatcher'
import type { SkillsApprove } from './generated/skills.approve'
import type { SkillsInstall } from './generated/skills.install'
import type { SkillsList } from './generated/skills.list'
import type { SkillsPreview } from './generated/skills.preview'
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

  // Reading, never writing: the backend fetches the document at this address,
  // parses it, refuses what it must and answers with the body and every scan
  // finding. Nothing is installed until the person says so, which is why the
  // preview is a method of its own rather than a flag on an install.
  preview(url: string): Promise<SkillsPreview> {
    return this.dispatcher.call<SkillsPreview>('skills.preview', { url })
  }

  // Adopting what was just read. The address is the WHOLE request — there is
  // deliberately no body, name or digest to send — because the backend
  // fetches it a second time and compares against the document its own
  // preview showed. A renderer that handed the bytes back would be asserting
  // what the person approved, and the digest recorded has to be over what was
  // actually written.
  install(url: string): Promise<SkillsInstall> {
    return this.dispatcher.call<SkillsInstall>('skills.install', { url })
  }
}
