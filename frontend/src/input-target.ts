// Pluggable input targets (ADR-0004 §3). The editor is a passive surface; a
// registered InputTarget decides where a submitted document goes. New kinds
// (shell now, LLM agent later) are added by registering a target, never by
// editing the editor.
export interface SubmitContext {
  readonly targetId: string
}

export interface InputTarget {
  readonly id: string
  readonly label: string
  submit(doc: string, ctx: SubmitContext): Promise<void>
}

export interface InputTargetRegistry {
  register(target: InputTarget): void
  setActive(id: string): void
  active(): InputTarget
}

export function createRegistry(): InputTargetRegistry {
  const targets = new Map<string, InputTarget>()
  let activeId: string | undefined
  return {
    register(target) {
      targets.set(target.id, target)
      if (activeId === undefined) activeId = target.id
    },
    setActive(id) {
      if (!targets.has(id)) throw new Error(`input-target: unknown id ${id}`)
      activeId = id
    },
    active() {
      if (activeId === undefined) throw new Error('input-target: no target registered')
      return targets.get(activeId)!
    },
  }
}
