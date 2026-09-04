import type { Skill as GeneratedSkill, SkillsList } from './generated/skills.list'
import type { SkillsFile } from './generated/skills.file'
import type { SkillsInstall } from './generated/skills.install'
import type { SkillsPreview } from './generated/skills.preview'

export type Skill = GeneratedSkill

export interface SkillsClientLike {
  list(): Promise<SkillsList>
  setEnabled(name: string, enabled: boolean): Promise<unknown>
  remove(name: string): Promise<unknown>
  approve(name: string): Promise<unknown>
  preview(url: string): Promise<SkillsPreview>
  install(url: string): Promise<SkillsInstall>
  file(name: string, path: string): Promise<SkillsFile>
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

  // Reading REFRESHES NOTHING, deliberately: skills.preview writes nothing,
  // so a list that changed after one would be a list that changed for a
  // reason nobody can name. It is a passthrough rather than a call the
  // surface makes on a client of its own, and that is what makes the pair
  // answerable: skills.install compares against a digest the SERVER kept
  // from its own preview, so the two calls have to reach one backend over
  // one connection. Handing the surface a store for the write and a client
  // for the read would be two collaborators that can differ.
  preview(url: string): Promise<SkillsPreview> {
    return this.client.preview(url)
  }

  // Reading one file REFRESHES NOTHING either, and for the same reason
  // `preview` does not: skills.file writes nothing, so a list that changed
  // after a read would be a list that changed for a reason nobody can name.
  // It is a passthrough rather than a call the surface makes on a client of
  // its own, so that the page has ONE collaborator for skills — a surface
  // holding a store for the writes and a client for the reads is two things
  // that can be pointed at different backends.
  file(name: string, path: string): Promise<SkillsFile> {
    return this.client.file(name, path)
  }

  // Installing goes through the store because the list is now different, and
  // the caller still gets what was installed so the dialog can name it. The
  // refresh is awaited BEFORE the result is returned, so a caller that closes
  // its dialog on this promise cannot close it over a list that has not
  // caught up.
  async install(url: string): Promise<SkillsInstall> {
    const installed = await this.client.install(url)
    await this.refresh()
    return installed
  }
}
