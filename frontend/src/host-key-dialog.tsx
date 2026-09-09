import { Show } from 'solid-js'
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

interface HostKeyDialogProps {
  evidence: HostKeyDecisionEvidence
  busy: boolean
  onAccept: () => void
  onClose: () => void
}

/** One consent surface for probe-time and open-time host-key decisions. */
export function HostKeyDialog(props: HostKeyDialogProps) {
  return (
    <Dialog
      open
      onClose={props.onClose}
      title={props.evidence.changed ? 'Host key changed' : 'Unknown host key'}
      footer={
        <>
          <Button
            variant={props.evidence.changed ? 'danger' : 'primary'}
            disabled={props.busy}
            onClick={props.onAccept}
          >
            {props.busy
              ? 'Trusting…'
              : props.evidence.changed
                ? 'Trust the new key'
                : 'Trust host key'}
          </Button>
          <Button variant="default" disabled={props.busy} onClick={props.onClose} autofocus>
            Cancel
          </Button>
        </>
      }
    >
      <Stack>
        <Show
          when={props.evidence.changed}
          fallback={
            <p>
              This is the first time nocx has met this host. If you trust this machine, accept its
              key — it will be saved to ~/.ssh/known_hosts.
            </p>
          }
        >
          <p>
            The host key offered by this server differs from the one nocx has seen before. That can
            mean the host&rsquo;s key was regenerated — or that someone is intercepting this
            connection. Do not continue unless you are sure this is the same machine. Accepting
            replaces the old key: it stops being trusted.
          </p>
        </Show>
        <p>
          {props.evidence.changed ? 'Stored fingerprint' : 'Offered fingerprint'}:{' '}
          <code>
            {props.evidence.changed ? props.evidence.storedFingerprint : props.evidence.fingerprint}
          </code>
        </p>
        <Show when={props.evidence.changed}>
          <p>
            Offered fingerprint: <code>{props.evidence.fingerprint}</code>
          </p>
        </Show>
      </Stack>
    </Dialog>
  )
}

interface AgentApprovalDialogProps {
  executable: string
  digest: string
  workspace: string
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
 * AND IT ADMITS WHAT NOCX CANNOT YET DO. The answer is durable and no surface
 * can withdraw it — nothing reads agent-approvals.json (nocx-6jbad). A person
 * deciding is entitled to know the decision is one-way for now, and a dialog
 * that implied otherwise would be the more comfortable lie.
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
              value: 'Until you undo it, and nocx has no way to undo it yet',
              note: 'The answer survives a restart. Removing it means editing agent-approvals.json in the profile by hand.',
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
