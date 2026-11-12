// What is LIVE in a set of tabs, and the sentence that names it
// (nocx-isoph.6; §4.1 and §4.4 of the workspaces UX design, rule D6).
//
// Closing a workspace closes all of its tabs, so the person is owed the one
// thing a count cannot give them: WHICH of those tabs is doing something.
// "Close 4 tabs?" is a number nobody can weigh; that one of the four is a
// running deploy and another is an ssh session into production is a fact they
// can act on. This module owns that naming and nothing else — it decides
// nothing, exactly like lineage.ts, because the whole point of the surface it
// feeds is to ASK rather than to decide.
//
// WHY THIS IS NOT lineage.ts. That module answers a question about an EDGE —
// which live tabs descend from this one — and its sentence says that closing
// a parent LEAVES them running. This one answers a question about a
// CONTAINER, and its sentence says the opposite: these go with it. Two
// questions, two answers; what they genuinely share is how many things a
// prompt may name before the list stops being readable, and that lives here
// once as `nameAtMost`, which lineage.ts imports.
//
// WHY A LOCAL SHELL AT A PROMPT IS NOT LIVE. Nothing is lost by closing it —
// a new one costs a keystroke. A running command is work in flight, and a
// session on another machine is a connection whose state (an agent, a jump
// host, a sudo timestamp, an interactive program's screen) is not free to
// recreate, whether or not a command is running in it at this instant. Those
// two are what a person needs named before they answer.

/** What one member tab of a workspace is doing, as far as a close is
 *  concerned. `command` and `host` are the pane's own answer (see
 *  `PaneContent.liveWork`); `label` is what the strip calls the tab, added by
 *  the layer that owns the strip — a content never knows its own tab. */
export interface WorkspaceMember {
  /** The tab's label, as the person sees it. */
  readonly label: string
  /** The command running in the foreground right now, or null. */
  readonly command: string | null
  /** The machine the tab is talking to (`user@host` or `host`), or null for
   *  a local session and for a tab that holds no session at all. */
  readonly host: string | null
}

/** How many things a prompt names before it starts counting. Past this the
 *  list stops being a list and becomes a wall, and the person can no longer
 *  read what they are being asked about. One rule, because a second one in
 *  another module would drift and nobody would notice which. */
const NAMED_LIMIT = 5

/**
 * Join names into a phrase a person can read, naming at most `limit` of them
 * and counting whatever is left: `a, b, c, d, e and 2 more`.
 *
 * Takes names, never objects: what a thing is called is the caller's
 * question, and the two callers answer it differently.
 */
export function nameAtMost(names: readonly string[], limit = NAMED_LIMIT): string {
  const named = names.slice(0, limit)
  const rest = names.length - named.length
  return rest > 0 ? `${named.join(', ')} and ${rest} more` : named.join(', ')
}

/** A value the pane could not answer. '' and null are the same absence: a
 *  content with no host reports an empty string and a content with no session
 *  reports null, and neither is something to name. */
function present(value: string | null): string | null {
  const trimmed = (value ?? '').trim()
  return trimmed === '' ? null : trimmed
}

/** Whether this tab is doing something a person would want to know about
 *  before it dies — see the header for why an idle local shell is not. */
function isLive(member: WorkspaceMember): boolean {
  return present(member.command) !== null || present(member.host) !== null
}

/**
 * What to call one live tab: the command it is running, or — when it is live
 * because of where it is rather than what it is doing — the tab's own label,
 * qualified in both cases by the machine.
 *
 * The label is the fallback rather than the first choice because a command is
 * the more specific fact: `“ansible-playbook deploy.yml” on prod-01` says
 * what dies, where `“~” on prod-01` says only that something does.
 */
function nameOf(member: WorkspaceMember): string {
  const what = present(member.command) ?? present(member.label) ?? 'a tab'
  const host = present(member.host)
  return host === null ? `“${what}”` : `“${what}” on ${host}`
}

/** The same live-work rule applies to a single intentional tab close. */
export function closingTabMessage(member: WorkspaceMember): string | null {
  return isLive(member) ? `Closing this tab ends ${nameOf(member)}. Close it?` : null
}

/**
 * The question put to a person closing a workspace: how many tabs go, and
 * what is live among them.
 *
 * A workspace holds at least one tab for its whole life (§4.1), so the count
 * is never zero and no empty-workspace wording exists to get wrong.
 *
 * THE ALL-IDLE CASE SAYS SO, and that is a requirement rather than a
 * courtesy: the close still asks — closing several tabs at once is not
 * something to do silently — and a dialog that named an empty list would be
 * worse than one that says there is nothing running, because the person would
 * be left wondering what it failed to tell them.
 */
export function closingWorkspaceMessage(
  workspaceName: string,
  members: readonly WorkspaceMember[],
): string {
  const name = present(workspaceName)
  const subject = name === null ? 'this workspace' : `“${name}”`
  const count = members.length === 1 ? '1 tab' : `${members.length} tabs`
  const head = `Closing ${subject} closes ${count}.`

  const live = members.filter(isLive).map(nameOf)
  if (live.length === 0) return `${head} Nothing is running in them.`
  return `${head} Still running: ${nameAtMost(live)}. They are closed with it.`
}
