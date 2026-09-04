/**
 * Assistant permissions client — the ONE global policy (ADR-0020 §7 as amended
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
  Rule,
  Scope,
} from './generated/policy.get'
import type { PolicySet } from './generated/policy.set'
import type { PolicySetRule } from './generated/policy.setRule'
import type { PolicyForgetRule } from './generated/policy.forgetRule'
import type { PolicyExplain, Step, Scope as ExplanationScope } from './generated/policy.explain'

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
  /** The ids of the standing answers that are INERT: a loose permit saved
   *  under an earlier reading of commands, which grants nothing until a
   *  person has re-read what it now means and said so (design §5.6).
   *
   *  THE BACKEND'S ANSWER AND ONLY THE BACKEND'S, for the reason `live` is.
   *  The predicate joins three facts about a rule to the reading of commands
   *  running now — a version number no result carries — and
   *  `content.RulesNeedingConfirmation` is its one implementation. A second
   *  one here would agree everywhere anyone looked and disagree somewhere
   *  nobody did, while telling a person a permission works; a rule that
   *  quietly stopped working and says nothing about it is the soft degrade
   *  AGENTS.md forbids. Always an array. */
  awaitingReview: string[]
  /** The standing answers over command invocations, in document order, with
   *  the provenance that makes each one an object a person can take back:
   *  when it came into being, whether they ANSWERED it at a prompt or it was
   *  written into the policy, and the reading of commands it was agreed
   *  under. `policy.get` has carried them since the rules landed; nothing
   *  read them, so an answer a person gave was visible only to the code that
   *  enforced it. Always an array — the wire omits the key when there are
   *  none, and a surface must not have to tell absent from empty. */
  rules: PolicyRule[]
}

/** One standing answer, exactly as the wire declares it. The generated type
 *  is the only declaration; naming it here is what lets a surface hold one
 *  without re-describing the shape. */
export type PolicyRule = Rule

/**
 * What a caller may say about a rule it is writing: what the rule SAYS, and
 * nothing about where it came from.
 *
 * The absences are the design. `createdAt`, `source` and `evaluatorVersion`
 * are facts the backend records about the write — a renderer that could set
 * them could dress a rule it wrote as one a person answered at a prompt. The
 * id is the same rule one step further: leaving it out writes a NEW rule and
 * the backend mints its identity (AD-7); naming one replaces the rule already
 * wearing it, and an id naming nothing is refused. A page may take back what
 * it can see and may not choose the name of what it creates.
 */
export interface PolicyRuleWrite {
  id?: string
  selector: PolicyRule['selector']
  decision: PolicyRow['decision']
  grantedUnder?: PolicyRule['grantedUnder']
}

/**
 * Why one decision came out the way it did: the outcome, and the steps the
 * BACKEND's evaluator took, in the order it took them.
 *
 * Exactly as the wire declares it. Naming it here is what lets a surface hold
 * an explanation without re-describing its shape — and re-describing the shape
 * is one short step from re-deriving the content.
 */
export type PolicyExplanation = PolicyExplain

/** One step of an explanation: what the evaluator did, and what it did it to.
 *  A step names effects, rules and resources, and never a tool. */
export type PolicyExplanationStep = Step

/** The one resource an explanation points at, when one fell outside — in the
 *  scope form a row states it in, so a widening answer can be written from the
 *  explanation alone. */
export type PolicyExplanationResource = ExplanationScope

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
    return this.dispatcher.call<PolicyGet>('policy.get', {}).then((r) => ({
      matrix: toMatrix(r.policy),
      live: r.live,
      awaitingReview: r.awaitingReview,
      // The wire omits an empty rules array (`omitempty` on the Go side), and
      // a surface must not have to tell absent from empty to draw a list.
      rules: r.policy.rules ?? [],
    }))
  }

  /**
   * Write the MATRIX. It carries seven rows and nothing else — `PolicyMatrix`
   * has no rules key to send, and the backend refuses a document that names
   * one.
   *
   * That is not belt and braces, it is the fix for a defect that shipped: a
   * matrix save deleted every standing answer a person had approved, because
   * the whole document went over the wire and the page's copy of it was
   * always a little stale. The prompt writes rules while this page is open;
   * a page that cannot say anything about rules cannot delete one.
   */
  set(policy: PolicyMatrix): Promise<PolicySet> {
    return this.dispatcher.call<PolicySet>('policy.set', { policy })
  }

  /**
   * Write ONE rule: add it, or replace the one wearing its id. Everything
   * else in the policy — the other rules, all seven rows — is untouched,
   * because the backend edits the document it holds rather than the copy
   * this page read.
   */
  setRule(rule: PolicyRuleWrite): Promise<PolicySetRule> {
    return this.dispatcher.call<PolicySetRule>('policy.setRule', { rule })
  }

  /**
   * Ask WHY: what the policy decides about one command, and every step the
   * backend's evaluator took to decide it, in the order it took them.
   *
   * The steps arrive; they are never worked out here. The precedence order —
   * the effect row first and a refusing row final before any rule is read,
   * then the rules with their two skip guards, then the resource layer — has
   * exactly one implementation, and it is the evaluator. A second one in
   * TypeScript would agree with it everywhere anyone looked and disagree
   * somewhere nobody did, which is how a saved host was once inserted over a
   * user's choice. `policy-explain-is-not-reimplemented.test.ts` is the test
   * that keeps this a rule rather than an intention.
   *
   * `effect` is the class the CALL classified as, which a surface was already
   * told (the approval prompt receives it); it is not derived here either.
   */
  explain(command: string, effect: PolicyExplain['effect']): Promise<PolicyExplanation> {
    return this.dispatcher.call<PolicyExplain>('policy.explain', { command, effect })
  }

  /**
   * Forget ONE rule by id. An id naming no rule RESOLVES with
   * `removed: false` rather than rejecting: the rule is already not there,
   * which is what forgetting asked for.
   */
  forgetRule(id: string): Promise<PolicyForgetRule> {
    return this.dispatcher.call<PolicyForgetRule>('policy.forgetRule', { id })
  }
}
