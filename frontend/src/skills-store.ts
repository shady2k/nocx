import type { Skill as GeneratedSkill, SkillsList } from './generated/skills.list'
import type { SkillsFile } from './generated/skills.file'
import type { SkillsFiles } from './generated/skills.files'
import type { SkillsAudit } from './generated/skills.audit'

export type Skill = GeneratedSkill

export interface SkillsClientLike {
  list(): Promise<SkillsList>
  setEnabled(name: string, enabled: boolean): Promise<unknown>
  remove(name: string): Promise<unknown>
  approve(name: string): Promise<unknown>
  file(name: string, path: string): Promise<SkillsFile>
  files(name: string): Promise<SkillsFiles>
  audit(name: string): Promise<SkillsAudit>
}

export type SkillsState =
  | { kind: 'loading' }
  | { kind: 'ready'; skills: readonly Skill[]; documentPath: string }
  | { kind: 'unavailable'; message: string; documentPath: string }

export class SkillsStore {
  private current: SkillsState = { kind: 'loading' }
  private generation = 0
  private subscribers = new Set<(state: SkillsState) => void>()

  constructor(private readonly client: SkillsClientLike) {}

  subscribe(cb: (state: SkillsState) => void): () => void {
    this.subscribers.add(cb)
    cb(this.current)
    return () => this.subscribers.delete(cb)
  }

  private set(state: SkillsState): void {
    this.current = state
    for (const cb of this.subscribers) cb(state)
  }

  async refresh(): Promise<void> {
    const generation = ++this.generation
    try {
      const result = await this.client.list()
      if (generation !== this.generation) return
      if (result.documentError) {
        this.set({
          kind: 'unavailable',
          message: result.documentError,
          documentPath: result.documentPath,
        })
      } else {
        this.set({ kind: 'ready', skills: result.skills, documentPath: result.documentPath })
      }
    } catch (err) {
      if (generation !== this.generation) return
      this.set({
        kind: 'unavailable',
        message: err instanceof Error ? err.message : String(err),
        documentPath: '',
      })
    }
  }

  async setEnabled(name: string, enabled: boolean): Promise<void> {
    await this.client.setEnabled(name, enabled)
    await this.refresh()
  }

  async remove(name: string): Promise<void> {
    await this.client.remove(name)
    await this.refresh()
  }
  async approve(name: string): Promise<void> {
    await this.client.approve(name)
    await this.refresh()
  }

  // Reading one file REFRESHES NOTHING: skills.file writes nothing, so a
  // list that changed after a read would be a list that changed for a reason
  // nobody can name. It is a passthrough rather than a call the surface makes
  // on a client of its own, so that the page has ONE collaborator for skills
  // — a surface holding a store for the writes and a client for the reads is
  // two things that can be pointed at different backends.
  file(name: string, path: string): Promise<SkillsFile> {
    return this.client.file(name, path)
  }

  // Listing what a skill carries refreshes nothing either, for `file`'s
  // reason: skills.files writes nothing, so a list that changed after one
  // would be a list that changed for a reason nobody can name. It is a
  // passthrough so the page keeps ONE collaborator for skills.
  files(name: string): Promise<SkillsFiles> {
    return this.client.files(name)
  }

  // Asking for a reading refreshes nothing either, and here the reason is
  // the point rather than a consequence: skills.audit writes nothing, and it
  // must be VISIBLE that it writes nothing. The report changes no switch, no
  // digest and no status — what the assistant is offered is still the
  // person's switch and the digest comparison — so a refresh after one would
  // suggest the reading had moved something.
  audit(name: string): Promise<SkillsAudit> {
    return this.client.audit(name)
  }
}
