/**
 * Agent policy surface — the Settings page carrying the ONE global policy
 * (ADR-0020 §7 as amended, accepted 2026-08-16): a MATRIX of seven effect
 * classes, each one of permit | ask | refuse plus the resource scopes the
 * decision applies within.
 *
 * Three invariants are built into this surface rather than asserted in prose:
 *
 * - **The store decides.** There is no Save button and therefore no unsaved
 *   state: every gesture writes, and the page then adopts what a fresh read
 *   answers. It can never show a policy the store did not take, and a refused
 *   write leaves nothing behind to lose.
 * - **The kind select has NO 'tool' option.** The grant is over resources and
 *   effects, never over tool names (ADR-0028 decision 4), so the one
 *   vocabulary this page speaks cannot name a tool.
 * - **The words are the product's, and they are not this file's.** Row labels
 *   come from `effect-labels.ts`, which the approval prompt reads too — the
 *   question a person answers and the standing answer they later revoke must
 *   say the same thing.
 *
 * `live` is the backend's answer to "which of these rows govern anything
 * today", and the page draws those rows and no others. Five of the seven
 * classes have no declared tool behind them: nothing can produce them, so a
 * row offering to answer a question that cannot be asked is the fiction to
 * avoid — seven equal rows were the first version of it, and a disclosure
 * headed "capabilities the assistant does not have yet" was the second, which
 * left five controls on the page that did nothing. Not drawing them hides no
 * degrade (AGENTS.md), because there is no capability behind them to degrade;
 * and it needs no undoing later, since a row reappears the moment `live`
 * carries its effect.
 *
 * The one exception is a class that already CARRIES an answer, which is the
 * only case where drawing nothing would hide something real: an answer nobody
 * can see is an answer nobody can take back. Those keep their row, under a
 * heading that says they govern nothing yet.
 *
 * Working liveness out here is not an option — it would mean mapping a tool
 * name to an effect, which is the one thing no configuration path may do.
 */
import { createSignal, For, Index, onMount, Show } from 'solid-js'
import {
  blankPolicy,
  EFFECT_KEYS,
  type EffectKey,
  type PolicyClient,
  type PolicyMatrix,
  type PolicyRow,
  type PolicyView,
} from './policy-client'
import { effectHeading } from './effect-labels'
import {
  PageSection,
  Section,
  Select,
  TextField,
  Button,
  IconButton,
  Badge,
  StatusDot,
  showToast,
  type StatusDotTone,
} from './ui'
import { Spinner } from './ui/spinner'
import { PlusIcon, TrashIcon } from './ui/icons'

type Decision = PolicyRow['decision']

/** What each decision means to the person setting it. "Refuse" and "permit"
 *  are the wire's words; these are the product's. */
const DECISION_LABEL: Record<Decision, string> = {
  permit: 'Allowed',
  ask: 'Ask every time',
  refuse: 'Never',
}

/** The order the options are offered in — widest first, so the default sits
 *  in the middle where it reads as the middle. */
const DECISION_ORDER: Decision[] = ['permit', 'ask', 'refuse']

/** The tone of a standing decision's line. `ask` never renders one: it is the
 *  default, and a row that is on the default says nothing at all. */
const DECISION_TONE: Record<Decision, StatusDotTone> = {
  permit: 'warning',
  ask: 'neutral',
  refuse: 'neutral',
}

/** The scope kinds a policy may name. 'tool' is deliberately absent. */
const SCOPE_KINDS = ['environment', 'session', 'path', 'credential', 'destination'] as const
type ScopeKind = (typeof SCOPE_KINDS)[number]

export interface AgentPolicySectionProps {
  client: PolicyClient
}

/** A scope with no id is not a scope yet — it is a control someone opened.
 *  `ParseEffectPolicy` would refuse it, so it never reaches the wire. */
function withoutBlankScopes(m: PolicyMatrix): PolicyMatrix {
  const out = {} as PolicyMatrix
  for (const k of EFFECT_KEYS) {
    out[k] = { ...m[k], scopes: m[k].scopes.filter((s) => s.id.trim() !== '') }
  }
  return out
}

/** Whether two matrices say the same thing. Field-by-field rather than by
 *  serialising: a key-order difference between a wire row and an edited copy
 *  is not a change, and a comparison that thought so would write on every
 *  blur of an untouched field. */
function sameMatrix(a: PolicyMatrix, b: PolicyMatrix): boolean {
  return EFFECT_KEYS.every((k) => {
    const x = a[k]
    const y = b[k]
    return (
      x.decision === y.decision &&
      x.scopes.length === y.scopes.length &&
      x.scopes.every((s, i) => s.kind === y.scopes[i].kind && s.id === y.scopes[i].id)
    )
  })
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

export function AgentPolicySection(props: AgentPolicySectionProps) {
  /** What the store holds, as of the last read. The standing-decision line
   *  renders from THIS, never from the draft: a half-typed scope must not
   *  claim to be in force. */
  const [wire, setWire] = createSignal<PolicyMatrix>(blankPolicy())
  /** What the controls show. Identical to `wire` except while a scope is
   *  being typed — the one gesture on this page that is not instantaneous. */
  const [draft, setDraft] = createSignal<PolicyMatrix>(blankPolicy())
  const [live, setLive] = createSignal<EffectKey[]>([])
  const [loaded, setLoaded] = createSignal(false)
  const [loadError, setLoadError] = createSignal<string | null>(null)
  const [busyRow, setBusyRow] = createSignal<EffectKey | null>(null)

  /** Adopt a read whole: the matrix and the live list from the SAME answer.
   *  Two accessors filled separately would let the page render a row set and
   *  a liveness that never coexisted. */
  function adopt(view: PolicyView) {
    setWire(view.matrix)
    setDraft(view.matrix)
    setLive(view.live)
    setLoaded(true)
    setLoadError(null)
  }

  async function read() {
    try {
      adopt(await props.client.get())
    } catch (e) {
      setLoadError(messageOf(e))
    }
  }

  // The load happens once, on mount — the client prop never changes on a live
  // page, and a bare top-level read-and-chain would be an untracked
  // fire-and-forget the reactivity rule flags honestly (solid/reactivity).
  onMount(() => {
    void read()
  })

  const liveKeys = () => EFFECT_KEYS.filter((k) => live().includes(k))
  /** A class nothing can produce yet is NOT drawn. There is nothing to decide
   *  about it, and a row saying "Ask every time" about an effect no tool can
   *  have implies a question that will never be asked. `live` is the
   *  backend's answer, so a row returns the day a tool carrying that effect
   *  is declared — nothing here has to be remembered or undone for it.
   *
   *  The exception is a class that already CARRIES an answer, which is the
   *  one case where drawing nothing would hide something: an answer nobody
   *  can see is an answer nobody can take back. Those rows stay, grouped
   *  under the sentence that says they govern nothing yet, until the person
   *  sets them back to Ask. */
  const answeredDormantKeys = () =>
    EFFECT_KEYS.filter(
      (k) =>
        !live().includes(k) && !(wire()[k].decision === 'ask' && wire()[k].scopes.length === 0),
    )

  /**
   * Write one gesture and adopt the result.
   *
   * `policy.set` acknowledges; it does not echo the policy back, so the only
   * way to show what the store HOLDS rather than what we hoped it took is to
   * read again. That is the round trip, and it is deliberate: the alternative
   * — trusting the payload we just sent — is exactly the "page shows a policy
   * the store did not take" state this page was redrawn to remove.
   *
   * A gesture that would not change anything is not a write: leaving a blank
   * scope field is a gesture, and it must not cost a round trip.
   */
  async function commit(next: PolicyMatrix, key: EffectKey) {
    const payload = withoutBlankScopes(next)
    if (sameMatrix(payload, wire())) return
    setBusyRow(key)
    try {
      await props.client.set(payload)
      adopt(await props.client.get())
    } catch (e) {
      showToast({
        message: `That change was not saved: ${messageOf(e)}`,
        level: 'danger',
      })
      await read()
    } finally {
      setBusyRow(null)
    }
  }

  /** Edit one row of the draft and hand back the whole matrix, so a caller
   *  commits the value it just wrote rather than re-reading a signal. */
  function editRow(key: EffectKey, patch: Partial<PolicyRow>): PolicyMatrix {
    const next: PolicyMatrix = { ...draft(), [key]: { ...draft()[key], ...patch } }
    setDraft(next)
    return next
  }

  function editScope(
    key: EffectKey,
    i: number,
    patch: Partial<{ kind: ScopeKind; id: string }>,
  ): PolicyMatrix {
    return editRow(key, {
      scopes: draft()[key].scopes.map((s, n) => (n === i ? { ...s, ...patch } : s)),
    })
  }

  function renderRow(key: EffectKey) {
    const row = () => draft()[key]
    const standing = () => wire()[key].decision
    const busy = () => busyRow() === key

    return (
      <div class="st-policy__row" data-effect={key}>
        <span class="st-policy__effect">{effectHeading(key)}</span>
        <Select
          value={row().decision}
          disabled={busy()}
          options={DECISION_ORDER.map((d) => ({ value: d, label: DECISION_LABEL[d] }))}
          onChange={(v) => void commit(editRow(key, { decision: v as Decision }), key)}
        />
        {/* The row's scopes and their add control are ONE grid cell
            (nocx-c72pl). Emitted as direct children of the three-column row,
            the second scope wrapped into the next grid row's first column —
            the effect-label column — and rendered squeezed and misfiled under
            a different effect's name. */}
        <div class="st-policy__scopes">
          {/* Index, not For. `For` is keyed by REFERENCE, and every keystroke
              in a scope field mints a new scope object — so the row was torn
              down and rebuilt per character, taking the focused input with
              it. A person could type one character of "/workspace" and then
              be typing into nothing. `Index` keys by position and updates the
              value in place, which is what an edited list wants. */}
          <Index each={row().scopes}>
            {(scope, i) => (
              <div class="st-policy__scope">
                <Select
                  value={scope().kind}
                  disabled={busy()}
                  options={SCOPE_KINDS.map((k) => ({ value: k, label: k }))}
                  onChange={(v) => void commit(editScope(key, i, { kind: v as ScopeKind }), key)}
                />
                {/* Typed into freely, written when the person is DONE with it
                    — blur or Enter. ParseEffectPolicy rejects a non-absolute
                    path, so a per-keystroke write would be a refused write on
                    every character of "/workspace". */}
                <TextField
                  value={scope().id}
                  placeholder="/workspace or a session id"
                  onInput={(v) => editScope(key, i, { id: v })}
                  onCommit={(v) => void commit(editScope(key, i, { id: v }), key)}
                />
                <IconButton
                  ariaLabel={`Remove scope from ${effectHeading(key)}`}
                  onClick={() =>
                    void commit(
                      editRow(key, { scopes: row().scopes.filter((_, n) => n !== i) }),
                      key,
                    )
                  }
                >
                  <TrashIcon />
                </IconButton>
              </div>
            )}
          </Index>
          <Button
            variant="ghost"
            disabled={busy()}
            onClick={() => editRow(key, { scopes: [...row().scopes, { kind: 'path', id: '' }] })}
          >
            <PlusIcon /> Scope
          </Button>
        </div>
        {/* A row on the default says nothing: silence is the normal state, and
            a line under every row would be the "Vault is locked, three times"
            noise the kit's own README calls out. A row OFF the default says
            what it means, in the words the question used. */}
        <Show when={standing() !== 'ask'}>
          <p class="st-policy__state" data-tone={DECISION_TONE[standing()]}>
            <StatusDot tone={DECISION_TONE[standing()]} accessibleName="Standing answer">
              {`${effectHeading(key)} — ${DECISION_LABEL[standing()]}`}
            </StatusDot>
          </p>
        </Show>
      </div>
    )
  }

  return (
    <PageSection
      title="Agent policy"
      description="What the assistant may do on its own, and what it must ask you about first. Anything not set here is asked."
    >
      <Show when={loadError()}>
        <Badge tone="danger">{`Could not read the policy: ${loadError()}`}</Badge>
      </Show>
      <Show when={!loaded() && loadError() === null}>
        <Spinner size="sm" label="Loading the agent policy" />
      </Show>
      <Show when={loaded()}>
        <For each={liveKeys()}>{(key) => renderRow(key)}</For>
        <Show when={answeredDormantKeys().length > 0}>
          <div class="st-policy__dormant">
            <Section id="agent-policy-dormant" title="Capabilities the assistant does not have yet">
              <p class="st-policy__dormant-note">
                The assistant has no tool that does any of these, so these answers govern nothing
                today. They are kept, and each one applies the day it gains a tool that does it —
                set one back to Ask to take it back.
              </p>
              <For each={answeredDormantKeys()}>{(key) => renderRow(key)}</For>
            </Section>
          </div>
        </Show>
      </Show>
    </PageSection>
  )
}
