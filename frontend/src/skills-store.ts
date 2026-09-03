import type { Skill as GeneratedSkill, SkillsList } from './generated/skills.list'
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

  // Reading is the client's directly (nothing changes, so nothing needs
  // refreshing); installing goes through the store because the list is now
  // different, and the caller still gets what was installed so the dialog can
  // name it. The refresh is awaited BEFORE the result is returned, so a
  // caller that closes its dialog on this promise cannot close it over a list
  // that has not caught up.
  async install(url: string): Promise<SkillsInstall> {
    const installed = await this.client.install(url)
    await this.refresh()
    return installed
  }
}
