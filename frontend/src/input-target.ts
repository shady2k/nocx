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

const PASTE_START = '\x1b[200~'
const PASTE_END   = '\x1b[201~'

// ShellInputTarget routes a submitted document to the active PTY using the
// ADR-0004 §2 atomic handoff: the editor hides itself (caller's job), then the
// whole command is sent as ONE bracketed paste followed by CR. zle/readline
// paints the accepted command once as the committed transcript — no per-key
// echo, no stty, no readline mirroring.
export class ShellInputTarget implements InputTarget {
  readonly id = 'shell'
  readonly label = 'Shell'
  constructor(private readonly send: (data: string) => void) {}

  async submit(doc: string, _ctx: SubmitContext): Promise<void> {
    this.send(`${PASTE_START}${doc}${PASTE_END}\r`)
  }
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
