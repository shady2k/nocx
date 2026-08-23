// What a machine is called, when a person has to be told which one.
//
// ## Why this is a module and not a template string
//
// The tab strip's second line already answers it — `user@host` for a remote
// tab, the working directory for a local one — and the derivation lived
// inside `TerminalContent.hostLabel`, private to a 3000-line class. The
// operations list then needed the same answer for a different reason: it is
// GLOBAL, one list for every tab, so a row saying `/var/www` is meaningless
// the moment two connections are open. A second `${user}@${host}` next to
// the first is how two spellings of one machine start, and they agree
// everywhere anybody looks until the day one of them has no user.
//
// So the derivation moved here whole and the tab strip calls it. There is
// one owner of "what this machine is called", and every surface that names
// a machine gets the same string for the same machine.
//
// ## The local machine has a name too
//
// It is not blank. The ports panel already had the words — "This machine",
// the label it prints over the local target's listeners rather than letting
// a person infer the machine from a tab title — so those are the words, and
// they are here rather than there so the next surface uses them too.
//
// Two functions and not one, because the tab strip needs the EMPTY answer:
// its second line falls back to the working directory when there is no
// host, and a line reading "This machine" would displace the one fact it
// had. A surface that must always name a machine calls `machineName`; a
// surface that has something better to say when the machine is this one
// calls `remoteMachineName` and decides for itself.

/** What the local machine is called. The ports panel's words (ports.tsx),
 *  because a person should meet one name for one thing. */
export const THIS_MACHINE = 'This machine'

/**
 * The remote machine as a person writes it — `user@host`, or the bare host
 * when no user is known — and '' when there is no host, which is what a
 * local session has.
 *
 * The empty answer is deliberate and is the reason this is separate from
 * `machineName`: a caller with a better thing to say about the local
 * machine must be able to detect that case, and `'' || fallback` is how the
 * tab strip has always done it.
 */
export function remoteMachineName(
  user: string | null | undefined,
  host: string | null | undefined,
): string {
  if (!host) return ''
  return user ? `${user}@${host}` : host
}

/**
 * The machine, always named — `user@host`, the bare host, or `THIS_MACHINE`.
 *
 * For a surface that must say WHICH machine whatever the answer is: an
 * operations row is read minutes later, out of the context of the tab that
 * started the work, so "the one without a host" is not a thing a person can
 * be left to infer.
 */
export function machineName(
  user: string | null | undefined,
  host: string | null | undefined,
): string {
  return remoteMachineName(user, host) || THIS_MACHINE
}
