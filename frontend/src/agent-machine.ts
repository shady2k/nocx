/**
 * HOW A TRUST DOMAIN IS SAID TO A PERSON (nocx-50w7p.16).
 *
 * ONE OWNER, because two surfaces word the same machine — the dialog that asks
 * whether an agent may use nocx's tools, and the Settings page that reads the
 * answers back — and a second copy would be the second vocabulary AGENTS.md
 * names: the two agree everywhere anybody looks and disagree somewhere nobody
 * did.
 *
 * The facts travel (contracts/agentAccess.machine's shape, declared once and
 * copied verbatim into the three schemas that carry it); the words live here.
 * Nothing is parsed out of a composed string, and the durable key a machine is
 * part of never reaches the renderer.
 */
import type { HostRequest } from './generated/host.request'

/**
 * The machine facts, taken from the GENERATED contract and never restated: a
 * hand-written copy can want a field the wire does not carry.
 */
export type MachineFacts = NonNullable<HostRequest['machine']>

/** The machine as a person reads it, in a sentence: where the agent would run. */
export function machineLabel(machine: MachineFacts): string {
  if (machine.kind === 'ssh') {
    // Both are required by the schema for an ssh machine, but a surface must
    // not draw "undefined@undefined" if a build ever sends less: the fallback
    // says what is known rather than assembling nonsense from nothing.
    if (machine.host && machine.account) {
      return `${machine.account}@${machine.host}`
    }
    return 'another machine'
  }
  return 'this machine'
}

/**
 * The machine as a ROW IDENTITY: two machines' answers for one executable are
 * two rows, and a key that could not tell them apart would let one row's
 * button act on the other.
 */
export function machineKey(machine: MachineFacts): string {
  if (machine.kind === 'ssh') {
    return `ssh:${machine.host ?? ''}:${machine.account ?? ''}:${machine.hostKey ?? ''}`
  }
  return 'local'
}
