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
// whole command is sent as ONE bracketed paste followed by CR. zle/readline
// paints the accepted command once as the committed transcript — no per-key
// echo, no stty, no readline mirroring.
//
// When bracketed-paste mode IS on, \n within the paste is preserved as a
// literal command separator, so bash executes every line — a multi-line
// editor composition runs all commands, not just the last (nocx-4ff.14).
// When mode IS off the wrappers leak but the shell interprets \n as accept-
// line, which also executes every line.  Either way, the user gets the
// entire composed command.
const PASTE_START = '\x1b[200~'
const PASTE_END = '\x1b[201~'

export class ShellInputTarget implements InputTarget {
  readonly id = 'shell'
  readonly label = 'Shell'
  constructor(private readonly sendRaw: (data: string) => void) {}

  submit(doc: string): Promise<void> {
    this.sendRaw(`${PASTE_START}${doc}${PASTE_END}\r`)
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
