/**
 * AgentApprovalPrompt — the renderer half of an approval question (nocx-z9hj4,
 * design §7.2/§7.3): a run suspended and a person is being asked to decide.
 * One kind of question whether the risk was an effect coming in (a policy
 * escalation) or a secret going out (an egress finding) — the wire sends
 * agent.approvalRequested either way, and this surface renders it either way.
 *
 * Since nocx-gycwo an answer has a WIDTH as well as a direction. Before it,
 * every answer was `once` and bound to the exact proposal by call id and
 * argument hash, so a person who had just allowed the assistant to read the
 * screen was asked about the SAME screen on their next question, and the only
 * place to say "stop asking me this" was a settings page in a vocabulary they
 * had never seen. A policy question now offers allow and deny at once, in this
 * session and always — the logic every other assistant has — and the BACKEND
 * applies the width: this surface never edits the policy matrix, which would
 * put a second owner on the document the settings page owns (design §"Three
 * wire changes").
 *
 * Two things it must never do. It must never derive the effect from the tool
 * name — `effect` is on the wire precisely so it does not, because a rule
 * keyed by a tool name is what ADR-0028 decision 4 forbids. And it must never
 * offer a standing answer to an EGRESS question: an egress ask means a tool
 * result contained secret-shaped material and nothing has reached the model
 * provider yet, so "always" there would mean "always send secrets to the
 * provider", which is not a decision anyone should make by clicking a button
 * sitting next to five others. Egress keeps Allow / Deny, once only.
 *
 * What the surface must not overstate (design §7.2): approving covers the
 * call that is asking — it has NOT run, and no call after it in that response
 * will. It does NOT promise the domain is untouched: a permitted sibling
 * earlier in the same batch has already run. The sentence is on the surface,
 * where a person deciding reads it.
 */
import { For, Show } from 'solid-js'
import { ActionGroup, Badge, Button, CodeBlock, Prompt, Stack } from './ui'
import { EFFECT_LABEL } from './effect-labels'
import type { AgentApprovalRequested } from './generated/agent.approvalRequested'
import type { AgentApprove } from './generated/agent.approve'

/** How far an answer reaches — the wire's own vocabulary, not a second one. */
type ApprovalScope = AgentApprove['scope']

export interface AgentApprovalPromptProps {
  open: boolean
  /** The question as the backend sent it — the full binding plus the ask. */
  ask: AgentApprovalRequested
  /** The decision is in flight; the buttons are disabled. */
  busy: boolean
  /**
   * The person's answer: a direction and a width, in one act. One callback
   * rather than six, because the caller's job is to put both on the wire and
   * a surface that split them would invite a call site that forgot one.
   */
  onDecide: (approved: boolean, scope: ApprovalScope) => void
}

const TITLE: Record<AgentApprovalRequested['reason'], string> = {
  policy: 'This action needs your approval',
  egress: 'A tool result contained secret-shaped material',
}

/**
 * The three widths, narrowest first, in the order both groups read.
 *
 * `once` leads because it is the answer a hurried person should land on: it
 * decides this proposal and commits to nothing. It is also what the prompt
 * focuses on open, since Prompt puts the caret on the first enabled button.
 */
const SCOPES: ReadonlyArray<{ scope: ApprovalScope; suffix: string }> = [
  { scope: 'once', suffix: 'once' },
  // "in this session", never "in this pane": the permission binds to the
  // terminal session, so restarting the shell is a new session and the
  // question comes back. Naming the pane would promise a lifetime it has not.
  { scope: 'session', suffix: 'in this session' },
  { scope: 'always', suffix: 'always' },
]

export function AgentApprovalPrompt(props: AgentApprovalPromptProps) {
  const ask = () => props.ask
  const effectLabel = () => EFFECT_LABEL[ask().effect]

  const egressIntro = () => {
    if (ask().reason !== 'egress') return ''
    return ask().wasError
      ? 'The tool failed, and its error mentioned secret-shaped material. Nothing was sent to the model provider.'
      : 'A tool result contained secret-shaped material. Nothing was sent to the model provider.'
  }

  /**
   * One group of three. `variant` is spent on the narrowest answer in each
   * group and nowhere else: the two once-scoped buttons are the immediate
   * reply to the question being asked, and the standing answers are
   * deliberately quieter, because a decision that outlives the question
   * should be a deliberate act rather than the thing the eye lands on.
   */
  const group = (approved: boolean, verb: string, variant: 'primary' | 'danger') => (
    <ActionGroup ariaLabel={approved ? 'Allow this action' : 'Refuse this action'}>
      <For each={SCOPES}>
        {({ scope, suffix }) => (
          <Button
            variant={scope === 'once' ? variant : 'default'}
            disabled={props.busy}
            onClick={() => props.onDecide(approved, scope)}
          >
            {verb} {suffix}
          </Button>
        )}
      </For>
    </ActionGroup>
  )

  return (
    <Prompt
      open={props.open}
      // Escape and a click on the scrim are the NARROWEST refusal. Dismissing
      // a question is not answering it for every call to come.
      onClose={() => props.onDecide(false, 'once')}
      ariaLabel={TITLE[ask().reason]}
      placement="top-sheet"
      title={TITLE[ask().reason]}
      actionsLayout={ask().reason === 'egress' ? 'row' : 'stacked'}
      actions={
        <Show
          when={ask().reason === 'policy'}
          fallback={
            <>
              <Button
                variant="primary"
                disabled={props.busy}
                onClick={() => props.onDecide(true, 'once')}
              >
                Allow
              </Button>
              <Button
                variant="danger"
                disabled={props.busy}
                onClick={() => props.onDecide(false, 'once')}
              >
                Deny
              </Button>
            </>
          }
        >
          {group(true, 'Allow', 'primary')}
          {group(false, 'Deny', 'danger')}
        </Show>
      }
    >
      <Stack>
        <Show when={ask().reason === 'egress'}>
          <p>{egressIntro()}</p>
        </Show>
        <Show when={ask().reason === 'policy'}>
          <p>
            The assistant wants to <strong>{effectLabel()}</strong>.
          </p>
        </Show>
        <p>
          The assistant is asking to call <strong>{ask().tool}</strong> with these arguments:
        </p>
        <CodeBlock ariaLabel={`Arguments of ${ask().tool}`}>{ask().arguments}</CodeBlock>
        <Show when={(ask().findings?.length ?? 0) > 0}>
          <Stack gap="loose">
            <p>What was found, and where — never the material itself:</p>
            <For each={ask().findings}>
              {(f) => (
                <p>
                  <Badge tone={f.source === 'known' ? 'warning' : 'info'}>
                    {f.source === 'known' ? 'Known vault material' : 'Heuristic match'}
                  </Badge>{' '}
                  {f.source === 'known' ? f.secretName : f.kind}
                  {' — bytes '}
                  {f.start}–{f.end}
                </p>
              )}
            </For>
          </Stack>
        </Show>
        <p>
          Approving covers this call: it has not run, and no call after it in this response will. It
          does not promise the terminal is untouched — a permitted call earlier in this batch may
          already have run.
        </p>
        <Show when={ask().reason === 'policy'}>
          <p>
            An answer in this session lasts until this terminal session ends; restarting the shell
            starts a new one and the question comes back. An answer of always is a standing answer
            for <strong>{effectLabel()}</strong>, which you can change on the Agent policy page.
          </p>
        </Show>
      </Stack>
    </Prompt>
  )
}
