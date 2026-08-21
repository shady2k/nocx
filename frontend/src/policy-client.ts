/**
 * Agent policy client — the ONE global policy (ADR-0020 §7 as amended
 * 2026-08-16, accepted): the matrix the backend mints every agent run's
 * grant from.
 *
 * The wire shape is declared once, in contracts/policy.get.schema.json; the
 * renderer's types are generated and this module is their single consumer.
 * The generator inlines a structurally identical Row interface per effect
 * key, so `PolicyRow` below is the ONE uniform shape the surface reads and
 * writes — the policy is seven rows of `{decision, scopes}`, no other shape
 * exists.
 *
 * There is deliberately NO tool in this vocabulary: the wire's scope kinds
 * exclude 'tool', and the backend refuses a policy that names one. A
 * configuration that could say "permit readScreen" instead of "observe
 * within these sessions" would reintroduce the --no-tools mistake at the
 * settings layer one level up (ADR-0028 decision 4).
 */
import { Dispatcher } from './dispatcher'
import type {
  PolicyGet,
  Row as ObserveRow,
  Row1 as MutateReversibleRow,
  Row2 as MutateDestructiveRow,
  Row3 as PrivilegeChangeRow,
  Row4 as DiscloseRow,
  Row5 as CrossBoundaryRow,
  Row6 as DelegateRow,
  Scope,
} from './generated/policy.get'
import type { PolicySet } from './generated/policy.set'

type EffectDecision = 'permit' | 'ask' | 'refuse'

/** One matrix row: a decision for one effect class within its scopes. */
export interface PolicyRow {
  decision: EffectDecision
  scopes: Scope[]
}

/** The wire's row types are structurally identical; this union names every
 *  generated row export so the dead-export ratchet sees them consumed. */
type WireRow =
  | ObserveRow
  | MutateReversibleRow
  | MutateDestructiveRow
  | PrivilegeChangeRow
  | DiscloseRow
  | CrossBoundaryRow
  | DelegateRow

/** The wire form the backend always serves: PolicyGet['policy']. */
type WirePolicy = PolicyGet['policy']

/** The seven effect-class keys, in the wire's order. */
export const EFFECT_KEYS = [
  'observe',
  'mutate-reversible',
  'mutate-destructive',
  'privilege-change',
  'disclose',
  'cross-boundary',
  'delegate',
] as const

export type EffectKey = (typeof EFFECT_KEYS)[number]

/** The keyed matrix the wire carries. */
export type PolicyMatrix = Record<EffectKey, PolicyRow>

/**
 * What a read of the policy answers: the matrix, and which of its rows
 * govern anything at all.
 *
 * `live` is the backend's answer and can only be the backend's. Seven rows
 * are drawn and today only two have a declared tool behind them; working
 * that out here would mean mapping a tool name to an effect, which is the
 * one thing no configuration path may do (ADR-0028 decision 4). So the
 * registry's declaration table says it, once, on the wire — and a tool
 * declared tomorrow changes this list with no renderer edit at all.
 */
export interface PolicyView {
  matrix: PolicyMatrix
  /** The effect classes a declared tool carries, in the lattice's order. A
   *  row outside this list governs nothing yet, and the page says so rather
   *  than offering it as an equal to the rows that do. */
  live: EffectKey[]
}

/** A fresh all-ask matrix — what the backend serves when nothing is set. */
export function blankPolicy(): PolicyMatrix {
  const m = {} as PolicyMatrix
  for (const k of EFFECT_KEYS) m[k] = { decision: 'ask', scopes: [] }
  return m
}

/**
 * Adapts the wire's PolicyGet (distinct generated row types) into the one
 * uniform editor shape. The rows are structurally identical by construction;
 * this is the single translation from the generated vocabulary to the
 * surface's.
 */
function toMatrix(w: WirePolicy): PolicyMatrix {
  const m = {} as PolicyMatrix
  for (const k of EFFECT_KEYS) {
    const row: WireRow = w[k]
    m[k] = { decision: row.decision, scopes: row.scopes }
  }
  return m
}

export class PolicyClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  get(): Promise<PolicyView> {
    return this.dispatcher
      .call<PolicyGet>('policy.get', {})
      .then((r) => ({ matrix: toMatrix(r.policy), live: r.live }))
  }

  set(policy: PolicyMatrix): Promise<PolicySet> {
    return this.dispatcher.call<PolicySet>('policy.set', { policy })
  }
}
