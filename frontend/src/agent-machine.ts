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
  // No fallback for a half-named ssh machine, because the contract does not
  // admit one: the ssh branch REQUIRES host, account and hostKey (and the
  // backend refuses an ask that lacks them), so the fields below are always
  // there. A surface that had to invent wording here would be describing a
  // place nobody named, which is the thing this field exists to prevent.
  return machine.kind === 'ssh' ? `${machine.account}@${machine.host}` : 'this machine'
}

/**
 * The machine as a ROW IDENTITY: two machines' answers for one executable are
 * two rows, and a key that could not tell them apart would let one row's
 * button act on the other.
 */
export function machineKey(machine: MachineFacts): string {
  return machine.kind === 'ssh'
    ? `ssh:${machine.host}:${machine.account}:${machine.hostKey}`
    : 'local'
}
