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

// ShellInputTarget routes a submitted document to the active PTY using the
// ADR-0004 §2 atomic handoff: the editor hides itself (caller's job), then the
// command is pasted and a CR runs it. zle/readline paints the accepted command
// once as the committed transcript — no per-key echo, no stty, no mirroring.
//
// Bracketed-paste wrapping is DELEGATED to the terminal engine's paste(): it
// wraps in ESC[200~..ESC[201~ only when the shell has enabled mode 2004.
// Hand-rolling the wrappers here leaked them into the command whenever mode
// 2004 was not (yet) enabled — e.g. a fast second submit racing the prompt —
// producing corruption like `...00~` / `...01~` in the committed line.
export class ShellInputTarget implements InputTarget {
  readonly id = 'shell'
  readonly label = 'Shell'
  constructor(
    private readonly paste: (text: string) => void,
    private readonly sendRaw: (data: string) => void,
  ) {}

  submit(doc: string): Promise<void> {
    this.paste(doc)
    this.sendRaw('\r')
    return Promise.resolve()
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
