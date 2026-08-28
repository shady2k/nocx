import { For, Show } from 'solid-js'
import { render } from 'solid-js/web'
import type { JSX } from 'solid-js'
import { CodeBlock } from './code-block'
import { Dialog } from './dialog'
import { Stack } from './stack'

interface DumpDrive {
  text: string
  truncated: boolean
}

interface DumpData {
  request: DumpDrive[]
  response: DumpDrive[]
}

export interface DumpPanelProps {
  dump: DumpData
  copy: (text: string) => Promise<void>
  onClose: () => void
}

function DriveList(props: {
  label: string
  drives: DumpDrive[]
  copy: (text: string) => Promise<void>
}): JSX.Element {
  return (
    <section class="ui-dump-panel__drive">
      <h3>{props.label}</h3>
      <Show when={props.drives.length > 0} fallback={<p>No recorded drive.</p>}>
        <Stack>
          <For each={props.drives}>
            {(drive, index) => (
              <div class="ui-dump-panel__entry">
                <h4>Drive {index() + 1}</h4>
                <CodeBlock
                  ariaLabel={`${props.label} drive ${index() + 1}`}
                  wrap={false}
                  copy={props.copy}
                >
                  {drive.text}
                </CodeBlock>
                <Show when={drive.truncated}>
                  <p>Truncated at the 1 MiB capture limit.</p>
                </Show>
              </div>
            )}
          </For>
        </Stack>
      </Show>
    </section>
  )
}

export function DumpPanel(props: DumpPanelProps) {
  return (
    <Dialog open title="Model dump" size="lg" onClose={props.onClose}>
      <div class="ui-dump-panel">
        <Stack>
          <p class="ui-dump-panel__intro">
            Recorded provider bytes. Each drive is one model request and response.
          </p>
          <DriveList label="Request" drives={props.dump.request} copy={props.copy} />
          <DriveList label="Response" drives={props.dump.response} copy={props.copy} />
        </Stack>
      </div>
    </Dialog>
  )
}

export function mountDumpPanel(
  host: HTMLElement,
  props: Omit<DumpPanelProps, 'onClose'>,
): () => void {
  const lifecycle: { dispose: (() => void) | undefined } = { dispose: undefined }
  const close = () => {
    lifecycle.dispose?.()
    host.remove()
  }
  lifecycle.dispose = render(
    () => <DumpPanel dump={props.dump} copy={props.copy} onClose={close} />,
    host,
  )
  return () => {
    lifecycle.dispose?.()
    host.remove()
  }
}
