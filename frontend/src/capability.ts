import type { IntegrationDomain } from './lifecycle/domains'
import type { LifecycleState } from './lifecycle/state'

// Three independent axes replaced the single 'capability' value on
// 2026-08-04, after codex diagnosed the root cause of the rejected rail's
// offer-gate disagreement (nocx-atyf.1).
//
// The old model collapsed delivery path, observed shell state, and input
// presentation into one three-valued enum. A shell can emit useful markers
// while the user has deliberately selected terminal input; a shell can be
// ELIGIBLE for integration without nocx being AUTHORISED to inject it. One
// axis cannot say either thing.
//
// On 2026-08-05 (nocx-mlm7) the launch-policy enum (auto|ask|off) was
// itself replaced by three never-collapsed axes (spec §3.5):
//   - DesiredMode (auto|raw|script|relay): what the user wants nocx to do
//     with this destination, resolved by the profile cascade;
//   - ObservedDelivery (none|bootstrap-script|installed-script|relay): what
//     actually happened this session, read by the renderer from the markers;
//   - RelayConsent (unknown|granted|denied): persisted per destination,
//     consulted only for relay — script mode never reads it.
//
// The action set is derived ONLY after both authorisation and technical
// eligibility are resolved — the invariant that kills the worst defect in
// what was rejected: clicking an offered action never produces a
// prerequisite-rejection message.

/** What nocx observed about delivery this session — what actually happened,
 *  never what was intended. The second of the three axes (spec §3.5):
 *  'none' nothing arrived, 'bootstrap-script' the argv-staged launcher ran,
 *  'installed-script' the committed ~/.nocx/launch generation ran, 'relay'
 *  the Tier-B binary ran. Owned by the renderer, from the markers. */
export type ObservedDelivery = 'none' | 'bootstrap-script' | 'installed-script' | 'relay'

/** What the user wants nocx to do with this destination — the FIRST of the
 *  three axes (spec §3.5), resolved by the profile cascade and carried by
 *  the open ack. auto (the default, ADR-0033) is the name for "the user has
 *  not answered": it wraps and installs the scripts exactly as script does
 *  (N3), and additionally permits the relay to be OFFERED where a surface
 *  reaches for it (D8). script is that same answer given explicitly, and is
 *  never upgraded. relay allows the deployed binary and still integrates:
 *  the tiers are additive, so allowing it never withholds the scripts
 *  (§5.2). raw alone adds nothing — no rewrite, no remote write. There is
 *  no 'ask': asking is what auto does when no answer is stored for that
 *  host key. */
export type DesiredMode = 'auto' | 'raw' | 'script' | 'relay'

/** Relay consent for a destination — the THIRD of the three axes (spec
 *  §3.5). Persisted per destination; script mode never reads it. Relay
 *  without 'granted' behaves as raw. */
export type RelayConsent = 'unknown' | 'granted' | 'denied'

/** What nocx observes about the shell right now — semantic evidence, not
 *  keyboard ownership (that lives in input-state.ts). */
export type ShellState =
  | 'unsupported' // No markers have ever arrived; plain shell
  | 'eligible' // Markers arrived, shell speaks our protocol, nocx not
  // yet authorised for this destination
  | 'integrating' // In-band bootstrap is running
  | 'integrated' // Shell is fully integrated and providing markers
  | 'handover' // No domain owns the lane, but one is suspended below:
  // ownership is passing between two domains (nocx-mlyu)
  | 'lost' // Markers stopped unexpectedly (nested env, broken hook)
  | 'failed' // Integration attempt failed

/** What the user sees at the prompt. */
export type InputPresentation = 'editor' | 'terminal'

/** The connection-scope launch policy (nocx-4t37.2) was replaced by the
 *  DesiredMode axis (nocx-mlm7): auto, script and relay all integrate at
 *  session open; raw alone refuses every rewrite and remote write. There is
 *  no 'ask' — N3 makes the script footprint automatic product behaviour. */

/** One recovery action the UI may offer. Derived ONLY after both
 *  authorisation and technical eligibility are resolved — never disabled
 *  and never rejected at click time. */
export type RecoveryAction =
  { kind: 'enable-editor'; label: string } | { kind: 'restore-editor'; label: string }

/** The facts the action set is derived from. Authorisation and technical
 *  eligibility are separate gates, resolved BEFORE the action set is
 *  computed. */
export interface ActionFacts {
  shellState: ShellState
  presentation: InputPresentation
  observedDelivery: ObservedDelivery
  /** Has the user authorised nocx to own input at this destination? */
  authorized: boolean
  /** Is it technically safe for nocx to own input right now?
   *  (trusted prompt, not alt-screen, editor can show) */
  eligible: boolean
}

/**
 * Derive the actions the UI may offer.
 *
 * Returns empty when prerequisites are absent — no disabled-then-rejected
 * actions exist. The caller need only test array length to decide whether
 * a chip or menu item should appear at all.
 */
export function deriveActions(f: ActionFacts): RecoveryAction[] {
  // SEVERED (ADR-0024 §4): there is no in-band fallback tier, so no action
  // may offer integration or retry — a click that toasts "unavailable" is
  // exactly the disabled-then-rejected anti-pattern this module exists to
  // prevent. The integrate/retry branches are deleted with the in-band
  // machinery (nocx-u7uh.1); the migration bead reconnects the remaining
  // editor-presentation actions to authenticated facts.
  if (!f.authorized || !f.eligible) return []

  const actions: RecoveryAction[] = []

  // Integrated but the user is in terminal input: offer to switch back.
  if (f.shellState === 'integrated' && f.presentation === 'terminal') {
    actions.push({ kind: 'enable-editor', label: 'Enable command editor' })
    return actions
  }

  // Markers stopped unexpectedly: offer restoration.
  if (f.shellState === 'lost') {
    actions.push({ kind: 'restore-editor', label: 'Restore command editor' })
    return actions
  }

  // Integrated + editor = healthy state: no actions.
  return actions
}

/** Does the pane fall back to the conventional, unstructured grid? Exactly
 *  when no domain owns the lane and none is waiting below it. A handover is
 *  the carve-out (nocx-mlyu): ownership is PASSING between two domains, not
 *  absent, so the structured presentation stays up. Tearing it down there
 *  is what showed the whole terminal buffer — the previous attempt's output
 *  and its password prompt — twice per nested session. */
export function fallsToConventionalGrid(state: ShellState): boolean {
  return state !== 'integrated' && state !== 'handover'
}

// ── Derivation helpers ─────────────────────────────────────────────────

/** Derive the observed shell state from the kernel's lifecycle axis
 *  (ADR-0024 §6, the projection bead nocx-u7uh.7). The kernel is fed ONLY
 *  by the published fact, so this is what the authenticated channel
 *  concluded: a live domain — at a ready prompt or running an attempt — is
 *  the kernel's word that the shell is integrated; a Desynchronized domain
 *  is not live (decision 9) and Lost is dead, both reported as lost.
 *  Stream-derived markers never reach this derivation, and the boolean
 *  `trusted` it replaced is deleted.
 *
 *  Native alone cannot answer the question, which is why the lane's domain
 *  stack is the second argument (nocx-mlyu). The published fact says
 *  'native' for three different conditions — the shell never integrated,
 *  the active domain suspended, the top domain closed — and the renderer
 *  cannot tell them apart from the fact (protocol §9). The stack can: a
 *  suspended domain still sits on it, so Native over a non-empty stack is
 *  a handover between two domains, and only Native over an EMPTY stack is
 *  a genuinely conventional terminal. Answering the handover with
 *  'unsupported' is what tore the structured presentation down twice per
 *  nested ssh and showed the whole terminal buffer. A lane that fell to
 *  Lost has an emptied stack, so it stays lost. */
export function shellStateFromLifecycle(
  state: LifecycleState,
  stack: readonly IntegrationDomain[],
): ShellState {
  switch (state.kind) {
    case 'prompt_ready':
    case 'running':
      return 'integrated'
    case 'desynchronized':
    case 'lost':
      return 'lost'
    case 'native':
      return stack.length > 0 ? 'handover' : 'unsupported'
  }
}
