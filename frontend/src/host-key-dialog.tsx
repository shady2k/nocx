import { Show } from 'solid-js'
import { machineLabel, type MachineFacts } from './agent-machine'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { FactList } from './ui/fact-list'
import { Stack } from './ui/stack'

interface HostKeyDecisionEvidence {
  host: string
  changed: boolean
  fingerprint: string
  storedFingerprint?: string
}

/** The connect-time helper ask (ADR-0068): a machine with no consent record
 *  for its host-key fingerprint. Only the identity travels here — what a
 *  yes and a no each mean is fixed copy in the dialog below, the same way
 *  AgentApprovalDialog states its own grant rather than taking it as a prop. */
interface HelperConsentAskEvidence {
  fingerprint: string
}

interface HostKeyDialogProps {
  /** null when this ask is about the helper alone — the key is already
   *  trusted, so there is nothing here to show or accept about it. */
  evidence: HostKeyDecisionEvidence | null
  /** null when this ask is about the host key alone (the ordinary
   *  probe-time and open-time case, unchanged). Present together with
   *  evidence exactly when the SAME dial discovered both: one dialog then
   *  carries both questions, each with its own action, rather than raising
   *  a second dialog once the key is trusted (owner's decision, 2026-09-16). */
  helperAsk: HelperConsentAskEvidence | null
  busy: boolean
  /** Meaningful only when evidence is non-null. */
  onAcceptHostKey: () => void
  /** Meaningful only when helperAsk is non-null. approved=false is a
   *  recorded decline, not a cancel: the connection stays usable without
   *  the helper and this fingerprint is not asked again. */
  onDecideHelper: (approved: boolean) => void
  onClose: () => void
}

/** One consent surface for probe-time and open-time host-key decisions, and
 *  the connect-time helper ask (ADR-0068) — which rides the SAME surface
 *  because the two questions can arrive on the very same connect: a host
 *  whose key is already trusted raises no host-key dialog at all, so
 *  hanging the helper ask off that dialog alone would leave it unasked
 *  forever on an already-trusted host (owner's decision, 2026-09-16). */
export function HostKeyDialog(props: HostKeyDialogProps) {
  const title = () => {
    if (props.evidence) return props.evidence.changed ? 'Host key changed' : 'Unknown host key'
    return 'Use the helper for this connection?'
  }
  return (
    <Dialog
      open
      onClose={props.onClose}
      title={title()}
      footer={
        <>
          {/* The standalone host-key action is offered only when the helper
              is not also being asked about: when both are present, trusting
              the key is a SHARED precondition of either helper answer, so
              "Use the helper" and "Not now" each trust it as part of doing
              one thing — a third, partial-completion button would leave the
              dialog needing yet another click to finish either question. */}
          <Show when={!props.helperAsk && props.evidence}>
            {(evidence) => (
              <Button
                variant={evidence().changed ? 'danger' : 'primary'}
                disabled={props.busy}
                onClick={props.onAcceptHostKey}
              >
                {props.busy
                  ? 'Trusting…'
                  : evidence().changed
                    ? 'Trust the new key'
                    : 'Trust host key'}
              </Button>
            )}
          </Show>
          <Show when={props.helperAsk}>
            <Button
              variant="primary"
              disabled={props.busy}
              onClick={() => props.onDecideHelper(true)}
            >
              {props.busy ? 'Saving…' : 'Use the helper'}
            </Button>
            <Button
              variant="default"
              disabled={props.busy}
              onClick={() => props.onDecideHelper(false)}
            >
              Not now
            </Button>
          </Show>
          <Button variant="default" disabled={props.busy} onClick={props.onClose} autofocus>
            Cancel
          </Button>
        </>
      }
    >
      <Stack>
        <Show when={props.evidence} keyed>
          {(evidence) => (
            <>
              <Show
                when={evidence.changed}
                fallback={
                  <p>
                    This is the first time nocx has met this host. If you trust this machine, accept
                    its key — it will be saved to ~/.ssh/known_hosts.
                  </p>
                }
              >
                <p>
                  The host key offered by this server differs from the one nocx has seen before.
                  That can mean the host&rsquo;s key was regenerated — or that someone is
                  intercepting this connection. Do not continue unless you are sure this is the same
                  machine. Accepting replaces the old key: it stops being trusted.
                </p>
              </Show>
              <p>
                {evidence.changed ? 'Stored fingerprint' : 'Offered fingerprint'}:{' '}
                <code>{evidence.changed ? evidence.storedFingerprint : evidence.fingerprint}</code>
              </p>
              <Show when={evidence.changed}>
                <p>
                  Offered fingerprint: <code>{evidence.fingerprint}</code>
                </p>
              </Show>
            </>
          )}
        </Show>
        <Show when={props.helperAsk} keyed>
          {(ask) => (
            <>
              <p>
                This connection is set to decide automatically. Nocx has not been told whether it
                may install its helper on this machine — a small program that powers the Git panel
                and other features that need more than a plain shell.
              </p>
              <p>
                If you allow it, nocx installs and runs the helper on this machine the next time it
                is needed. If you decline, the connection still works as a plain terminal, and you
                are not asked again for this machine — change your mind later from the
                connection&rsquo;s settings.
              </p>
              <p>
                Fingerprint: <code>{ask.fingerprint}</code>
              </p>
            </>
          )}
        </Show>
      </Stack>
    </Dialog>
  )
}

interface AgentApprovalDialogProps {
  executable: string
  digest: string
  workspace: string
  // The machine the answer would be given for. The wire always sends one
  // (internal/transport sets it for every approval ask); it is optional here
  // only because the capability's params are shared, and a surface must say
  // what it cannot name rather than call an unknown machine local.
  machine?: MachineFacts
  busy: boolean
  onDecide: (approved: boolean) => void
}

/**
 * The consent surface that admits an agent's process tree to nocx's tools.
 *
 * IT SAYS WHAT A YES ALLOWS (nocx-fu18z). It used to state six facts and
 * answer none of the questions a person actually has. "the tool endpoint" is
 * vocabulary nobody outside this repository has met; the scope arrived as the
 * durable key it is part of ("tool-endpoint:workspace:default"); the digest
 * sat there with nothing to check it against and no reason given; and what
 * would BECOME POSSIBLE, which is the whole of what consent is for, appeared
 * in no word of it. So the lead is one sentence naming the three powers the
 * caller's grant actually carries — Delegate, Observe and MutateDestructive
 * over its own session and environment (internal/app/worker_auth.go
 * callerGrant) — and the rows say how far the answer reaches and how long it
 * lasts.
 *
 * D14's words are kept verbatim in the lead: the approval must read "allow
 * this agent and commands it launches", and it is the sentence that tells a
 * person the grant does not stop at the process they typed.
 *
 * IT ALSO SAYS WHAT A NO COSTS. Without that line Deny reads as the button
 * that breaks something: the agent still runs, it simply runs without nocx's
 * tools, and the pane says so.
 *
 * AND IT NAMES THE MACHINE THE ANSWER IS FOR (nocx-50w7p.16). A person's yes
 * admits an agent ON ONE MACHINE: the same executable on a host, or as another
 * account on one, is asked about again. A dialog that did not say where would
 * be collecting a decision about a place nobody named.
 *
 * AND IT SAYS WHERE THE ANSWER CAN BE UNDONE. It used to admit that no surface
 * could withdraw it, which was true when it was written and stopped being true
 * when the Settings page landed (Settings → Agent access, internal/transport's
 * agentAccess.forget, nocx-6jbad): a person deciding is entitled to know the
 * decision is revisitable, and a dialog that said otherwise would send them to
 * edit agent-approvals.json by hand for no reason.
 */
export function AgentApprovalDialog(props: AgentApprovalDialogProps) {
  return (
    <Dialog
      open
      onClose={() => props.onDecide(false)}
      title="Allow this agent to use nocx's tools?"
      footer={
        <>
          <Button variant="primary" disabled={props.busy} onClick={() => props.onDecide(true)}>
            {props.busy ? 'Saving…' : 'Allow'}
          </Button>
          <Button
            variant="default"
            disabled={props.busy}
            onClick={() => props.onDecide(false)}
            autofocus
          >
            Deny
          </Button>
        </>
      }
    >
      <Stack>
        <p>
          If you allow it, this agent <strong>and commands it launches</strong> can start other
          agents in new tabs, watch what they are doing, and stop them. They cannot read your
          secrets, reach another machine, or change anything else on this one.
        </p>
        <FactList
          ariaLabel="What is being allowed"
          facts={[
            { name: 'Agent', value: props.executable },
            {
              name: 'Machine',
              value: props.machine ? machineLabel(props.machine) : 'a machine nocx could not name',
              // THREE cases and not two: an ask with no machine is not an ask
              // about this machine. A note that fell through to the local
              // wording would describe a place the wire never named, which is
              // how a surface promises something nobody asked for.
              note: !props.machine
                ? 'nocx could not name the machine this question is about, so nothing here says where the agent would run.'
                : props.machine.kind === 'ssh'
                  ? 'The agent runs on that host, as that account. Your answer is remembered for that machine alone, so the same agent on another host — or as another account — is asked about again.'
                  : 'The agent runs on this machine. Your answer is remembered for this machine alone.',
            },
            {
              name: 'Fingerprint',
              value: props.digest,
              note: 'The answer is remembered against these bytes, so a replaced program is asked about again.',
            },
            {
              name: 'Applies to',
              value: `Every tab in the ${props.workspace} workspace`,
              note: 'Not only this tab: the same agent started anywhere in this workspace is allowed without asking again.',
            },
            {
              name: 'Lasts',
              value: 'Until you undo it in Settings → Agent access',
              note: 'The answer survives a restart. Undoing it there means the next start of this agent asks again.',
            },
          ]}
        />
        <p>
          If you deny, the agent still runs — without nocx's tools, and its pane says so. The answer
          is remembered for this agent alone.
        </p>
      </Stack>
    </Dialog>
  )
}
