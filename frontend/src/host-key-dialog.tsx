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
  scope: string
  busy: boolean
  onDecide: (approved: boolean) => void
}

/** One consent surface for admitting an executable process tree to tools. */
export function AgentApprovalDialog(props: AgentApprovalDialogProps) {
  return (
    <Dialog
      open
      onClose={() => props.onDecide(false)}
      title="Allow agent access?"
      footer={
        <>
          <Button variant="primary" disabled={props.busy} onClick={() => props.onDecide(true)}>
            {props.busy ? 'Saving…' : 'Allow this agent and commands it launches'}
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
          Allow this agent <strong>and commands it launches</strong> to use the tool endpoint?
        </p>
        {/* The kit's named-row list, not two hand-rolled paragraphs. A
            digest is one 64-character word, and a <code> in a dialog body
            inherits no wrapping — .nocx-dialog__message carries the
            word-break and only the message prop wears that class — so the
            executable ran off the dialog and took its closing bracket with
            it, behind a horizontal scrollbar. .ui-fact-list__value breaks
            anywhere, which is the whole reason this component exists: its
            first caller was the other approval surface, for facts of
            exactly this shape (nocx-n7xha). */}
        <FactList
          ariaLabel="What is being approved"
          facts={[
            { name: 'Executable', value: props.executable },
            { name: 'Scope', value: props.scope },
          ]}
        />
      </Stack>
    </Dialog>
  )
}
