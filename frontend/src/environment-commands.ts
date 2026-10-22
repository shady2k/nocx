import { isInteractiveTransition, extractDestination } from './ssh-transition'

/**
 * Recognise commands that enter a new shell environment from the line the
 * user submitted (ADR-0004 §2: the renderer knows what it sent — knowledge
 * of our own output, not inspection of the byte stream).
 *
 * The recognised set is conservative by design: a missed offer is invisible
 * and cheap; a wrong one is noise on every `sleep 5`. The default for an
 * unknown command is `false` — not an environment change.
 *
 * **Grows by addition.** When a new environment-entry command is needed,
 * add it here and to the test table in environment-commands.test.ts.
 *
 * The `ssh` arm delegates to ssh-transition.ts: ONE parser for "did the user
 * go somewhere and expect a shell", because two would drift.
 */

/** What we can honestly say about the environment a submitted line entered.
 *
 *  `label` is for a human: the destination as the user wrote it, which is
 *  the only thing we know without integration. We do NOT know the remote
 *  cwd until OSC 7 arrives, and inventing one would be worse than showing
 *  none — so a nested environment is a place with no directory until it
 *  tells us otherwise (nocx-695k.2). */
export interface EnvironmentEntry {
  /** The command family, for the tests and for future per-kind wording. */
  kind: 'ssh' | 'docker' | 'podman' | 'kubectl' | 'su' | 'sudo' | 'tmux' | 'screen' | 'nix-shell'
  /** `pi@192.0.2.93`, `docker exec …`'s container, `su bob`. */
  label: string
}

/** The environment a submitted line enters, or null when it enters none.
 *  Returns null for anything we cannot classify confidently — a remote
 *  command (`ssh host uptime`), a redirection or a pipeline is not a place
 *  the user is now sitting in. */
export function environmentEntry(line: string): EnvironmentEntry | null {
  const trimmed = line.trim()
  if (trimmed === '') return null
  // A redirection or a pipeline means the shell is doing something with the
  // output, not handing us a new interactive environment.
  if (/[|><&;]/.test(trimmed)) return null

  const tokens = trimmed.split(/\s+/)
  const first = tokens[0]

  if (first === 'ssh') {
    // ONE ssh parser for the product. nocx-atyf.3 grew a second one with a
    // proper tokenizer (quotes, redirection operators) and the correct table
    // of value-taking flags; this is the same question — "did the user go
    // somewhere and expect a shell" — so it delegates rather than keeping a
    // weaker copy that would drift.
    if (!isInteractiveTransition(trimmed)) return null
    const dest = extractDestination(trimmed)
    return dest ? { kind: 'ssh', label: dest } : null
  }
  if (first === 'tmux') return { kind: 'tmux', label: 'tmux' }
  if (first === 'screen') return { kind: 'screen', label: 'screen' }
  if (first === 'su') return { kind: 'su', label: tokens[1] ? `su ${tokens[1]}` : 'su' }
  if (first === 'nix-shell') return { kind: 'nix-shell', label: 'nix-shell' }

  if (tokens[1] === 'exec' && (first === 'docker' || first === 'podman' || first === 'kubectl')) {
    const target = tokens.slice(2).find((t) => !t.startsWith('-'))
    return { kind: first, label: target ? `${first}:${target}` : first }
  }
  if (first === 'sudo' && (tokens[1] === '-i' || tokens[1] === '-s')) {
    return { kind: 'sudo', label: 'root' }
  }
  return null
}
