// Pluggable input targets (ADR-0004 §3). The editor is a passive surface; a
// registered InputTarget decides where a submitted document goes. New kinds
// (shell now, LLM agent later) are added by registering a target, never by
// editing the editor.

import type { Extension } from '@codemirror/state'
export interface SubmitContext {
  readonly targetId: string
}

export interface InputTarget {
  readonly id: string
  readonly label: string
  /** Whether a submission to this target IS a shell command. The
   *  composition root runs the shell submit orchestration — keyboard
   *  handoff, ledger record, running block, lifecycle attempt — only for
   *  targets that say yes; the agent target's question owns its whole flow
   *  (frame mint, ask, answer block) and must never open an attempt or
   *  hand the grid the keys (nocx-x8s2.2). Consulted via
   *  InputTargetRegistry.active(), never a mode boolean. */
  readonly routesToShell: boolean
  submit(doc: string, ctx: SubmitContext): Promise<void>
  /**
   * The editor extensions this target supplies (design §8.8): the shell
   * mode, syntax highlighting and the completion surface for the shell
   * target; an agent target later supplies its own. Allow-listed — they
   * cannot override the W2 keymap. The editor stays passive: the caller
   * feeds these to the CommandEditor constructor; the editor never calls
   * this itself.
   */
  editorExtensions?(): Extension[]
}

export interface InputTargetRegistry {
  register(target: InputTarget): void
  setActive(id: string): void
  active(): InputTarget
}

// ShellInputTarget routes a submitted document to the active PTY using the
// ADR-0004 §2 atomic handoff: the editor hides itself (caller's job), then the
// renderer pastes the complete document before raw CR accepts it. The renderer
// owns mode-2004 wrapping because only the terminal engine knows whether the
// running shell enabled bracketed paste. Newlines stay in the single document,
// so multi-line compositions still execute every line (nocx-4ff.14).
export class ShellInputTarget implements InputTarget {
  readonly id = 'shell'
  readonly label = 'Shell'
  readonly routesToShell = true
  constructor(
    private readonly paste: (text: string) => void,
    private readonly sendRaw: (data: string) => void,
    /** The shell's editor extensions (highlighting + completion), composed
     *  at the root and carried through the target so the seam is exercised:
     *  the editor receives its surface from the target, never from itself. */
    private readonly extensions: Extension[] = [],
  ) {}

  editorExtensions(): Extension[] {
    return this.extensions
  }

  submit(doc: string): Promise<void> {
    this.paste(doc)
    this.sendRaw('\r')
    return Promise.resolve()
  }
}

export function createRegistry(
  onActiveChange?: (target: InputTarget) => void,
): InputTargetRegistry {
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
      // The caret indicator renders the active target (ADR-0004 §3's UI
      // chip). The registry has no other listeners, so the notification
      // IS the indicator's refresh signal — the surface that wired it
      // re-reads active() and repaints (nocx-4wtlh).
      onActiveChange?.(targets.get(activeId)!)
    },
    active() {
      if (activeId === undefined) throw new Error('input-target: no target registered')
      return targets.get(activeId)!
    },
  }
}
