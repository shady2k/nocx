/**
 * Sandbox permissions dialog — the one pre-launch modal (ADR-0031 §4.2).
 *
 * An imperative dialog, built on `Dialog` like every other modal in the app.
 * It shows the mandatory workspace and the persisted baseline of additional
 * writable folders; each baseline entry is checked by default and unchecking
 * one records an exact removal for this tab only. Ephemeral additions are
 * picked through the native directory picker and can be removed row-by-row.
 *
 * The result is the bounded permission DELTAS — `add`/`remove` — never the
 * baseline itself or any effective policy root. The backend is the sole
 * policy author; this surface only reports what the user changed.
 */
import { createSignal, For, type Component } from 'solid-js'
import { render } from 'solid-js/web'
import { Dialog } from './ui/dialog'
import { Button } from './ui/button'
import { Checkbox } from './ui/checkbox'
import { Field } from './ui/field'
import { Section } from './ui/section'
import { Stack } from './ui/stack'
import { EditableRowList } from './ui/row-list'
import { showToast } from './ui/toast'

/** The confirmed permission deltas: ephemeral additions and exact baseline
 *  removals. Both are always arrays (possibly empty). */
export interface SandboxPermissionsResult {
  readonly add: string[]
  readonly remove: string[]
}

/** The dialog's inputs: the mandatory workspace, the persisted baseline, and
 *  the native folder picker for ephemeral additions. */
export interface SandboxPermissionsOptions {
  readonly workspace: string
  readonly baseline: readonly string[]
  readonly openDirectory: () => Promise<{ path: string }>
}

const MAX_PERMISSION_PATHS = 32

const SandboxPermissionsDialog: Component<
  SandboxPermissionsOptions & { onResolve: (result: SandboxPermissionsResult | null) => void }
> = (props) => {
  // Baseline entries the user unchecked — exact canonical removals.
  const [removed, setRemoved] = createSignal<readonly string[]>([])
  // Ephemeral directories added for this tab.
  const [additions, setAdditions] = createSignal<readonly string[]>([])
  // While a native picker is open, the list affordances are inert.
  const [picking, setPicking] = createSignal(false)

  const toggleBaseline = (path: string, checked: boolean) => {
    setRemoved((prev) => (checked ? prev.filter((p) => p !== path) : [...prev, path]))
  }

  const removeAddition = (index: number) => {
    setAdditions((prev) => prev.filter((_, i) => i !== index))
  }

  const addDirectory = async () => {
    setPicking(true)
    try {
      const picked = await props.openDirectory()
      // Empty path = cancelled: a no-op.
      if (!picked.path) return
      // Picking an unchecked baseline entry means re-enable that grant. Do
      // not manufacture an add/remove conflict for the backend to reject.
      if (props.baseline.includes(picked.path)) {
        setRemoved((prev) => prev.filter((path) => path !== picked.path))
        return
      }
      if (additions().length >= MAX_PERMISSION_PATHS) {
        showToast({
          level: 'danger',
          message: `At most ${MAX_PERMISSION_PATHS} folders can be added for one tab.`,
        })
        return
      }
      setAdditions((prev) => (prev.includes(picked.path) ? prev : [...prev, picked.path]))
    } catch (err) {
      // An unavailable native runtime is visible rather than silent: the
      // surface cannot type a path by hand (ADR-0031 §4.2), so it says why.
      showToast({
        level: 'danger',
        message: `Could not open the folder picker: ${
          err instanceof Error ? err.message : String(err)
        }`,
      })
    } finally {
      setPicking(false)
    }
  }

  const confirm = () => {
    props.onResolve({ add: [...additions()], remove: [...removed()] })
  }

  const cancel = () => props.onResolve(null)

  const workspaceId = 'sandbox-permissions-workspace'

  return (
    <Dialog
      open
      onClose={cancel}
      title="Sandbox permissions"
      footer={
        <>
          <Button variant="default" onClick={cancel}>
            Cancel
          </Button>
          <Button variant="primary" onClick={confirm}>
            Open sandboxed tab
          </Button>
        </>
      }
    >
      <Stack gap="loose">
        <Field for={workspaceId} label="Workspace" orientation="vertical">
          <span id={workspaceId} class="sandbox-permissions-workspace">
            {props.workspace}
          </span>
        </Field>
        <Section title="Additional writable folders">
          <p class="sandbox-permissions-hint">
            Checked folders are writable in this tab. Uncheck one to remove it from this tab only —
            the workspace is always writable.
          </p>
          <div class="sandbox-permissions-baseline">
            <For each={props.baseline}>
              {(path) => (
                <Checkbox
                  checked={!removed().includes(path)}
                  onChange={(checked) => toggleBaseline(path, checked)}
                  label={path}
                />
              )}
            </For>
          </div>
          <EditableRowList
            rows={additions()}
            ariaLabel="Folders added for this tab"
            addLabel="Add folder for this tab"
            emptyLabel="No folders added for this tab."
            removeLabel={(i) => `Remove added folder ${i + 1}`}
            disabled={picking()}
            onRemove={removeAddition}
            onAdd={() => void addDirectory()}
            renderRow={(path) => <span class="sandbox-permissions-path">{path()}</span>}
          />
        </Section>
      </Stack>
    </Dialog>
  )
}

/**
 * Imperative sandbox permissions dialog — resolves to the confirmed deltas or
 * null when cancelled (Escape, outside click, or the Cancel button).
 *
 * Settles and disposes exactly once, and restores focus through `Dialog`'s own
 * overlay cleanup (the same deferred-dispose pattern as `showConfirm`).
 */
export function showSandboxPermissions(
  options: SandboxPermissionsOptions,
): Promise<SandboxPermissionsResult | null> {
  return new Promise((resolve) => {
    const host = document.createElement('div')
    document.body.appendChild(host)

    let dispose: (() => void) | null = null
    let settled = false

    const finish = (result: SandboxPermissionsResult | null) => {
      // Escape fires the cancel path and the disposer can run again on unmount;
      // the promise must resolve exactly once.
      if (settled) return
      settled = true
      // Deferred so Dialog's own cleanup — popOverlay and focus restore — runs
      // against a live root before it is torn down.
      queueMicrotask(() => {
        dispose?.()
        host.remove()
      })
      resolve(result)
    }

    dispose = render(
      () => (
        <SandboxPermissionsDialog
          workspace={options.workspace}
          baseline={options.baseline}
          openDirectory={options.openDirectory}
          onResolve={finish}
        />
      ),
      host,
    )
  })
}
