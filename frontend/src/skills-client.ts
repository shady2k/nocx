import type { Dispatcher } from './dispatcher'
import type { SkillsApprove } from './generated/skills.approve'
import type { SkillsAudit } from './generated/skills.audit'
import type { SkillsFile } from './generated/skills.file'
import type { SkillsFiles } from './generated/skills.files'
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

  // One file of one discovered skill, for any provenance including builtin:
  // reading is not writing, and the person may read what the assistant reads.
  //
  // The path is relative to the skill's own directory and is sent AS THE
  // PERSON'S REQUEST, never resolved here: whether it stays inside that
  // directory is settled once, by the backend, through the same containment
  // the assistant's read tool goes through. A renderer that cleaned or joined
  // the path first would be a second answer to that question, agreeing with
  // the first everywhere anybody looked.
  //
  // A file that is not text and a file larger than the read budget come back
  // as a RESOLVED result carrying `refusal`, not as a rejection — they are
  // true sentences about a file that exists, and the caller needs its path,
  // provenance and `maxBytes` to say them. Only a refusal of the request
  // itself (the file is gone, the path leaves the skill, no such skill)
  // rejects.
  file(name: string, path: string): Promise<SkillsFile> {
    return this.dispatcher.call<SkillsFile>('skills.file', { name, path })
  }

  // What the skill is MADE OF, so the card can list it and point `file` at
  // any of it. It is a method of its own rather than a field on `list`
  // because a directory walk per row on every refresh — and the list
  // refreshes after every toggle, delete and approve — would be paid to fill
  // a field one open card reads.
  //
  // It answers for a skill that is switched OFF, which is the case it exists
  // for: an installed skill lands inert precisely so the person can open it
  // and see what it carries before turning it on.
  files(name: string): Promise<SkillsFiles> {
    return this.dispatcher.call<SkillsFiles>('skills.files', { name })
  }

  // THE READING A PERSON ASKS FOR (design §7). It is a method of its own —
  // never a field the card fills on open — because it is a model call, and a
  // model call is money: `internal/profile/role.go` refuses to spend that
  // silently, and a page load is the silent spend in another costume.
  //
  // The name is the WHOLE request. Nothing about the model is a parameter:
  // which model reads a skill is the auditing role's assignment, resolved on
  // the backend in the one place a role becomes an (endpoint, model) pair, so
  // a renderer that named one would be a second answer to that question. The
  // result says which role actually answered, on which endpoint, because an
  // unassigned auditing role falls back to the answering one and must never
  // do it quietly.
  //
  // A reading that could not happen REJECTS — no model assigned, an endpoint
  // that is gone, a skill that vanished since the card opened. It is never an
  // empty report, because an empty report is indistinguishable from a clean
  // one, which is the whole reason this feature refuses to certify anything.
  audit(name: string): Promise<SkillsAudit> {
    return this.dispatcher.call<SkillsAudit>('skills.audit', { name })
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
