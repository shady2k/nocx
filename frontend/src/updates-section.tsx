/**
 * UpdatesSection — what the Updates page says beyond its control
 * (nocx-1ebrs.35.1).
 *
 * The page's channel SELECT is not rendered here: it is declared in Go
 * (`update.channel`, internal/settings) and this page owns that section, so
 * the declaration's row renders above this component exactly as it would on a
 * generated page — same control, same description, same search and modified
 * dot (settings-rail-pages.ts). What the declaration cannot say is a fact
 * about THIS BUILD: AppImage installs can self-update, while deb and rpm
 * installs update through their package manager. The distinction is said here,
 * on the page the update controls live on, from the platform the wire reports —
 * not guessed from the user agent and not buried in a log.
 */

import { Show, createResource } from 'solid-js'

import { PageSection, StatusCard } from './ui'
import type { AppAbout } from './generated/app.about'

export interface UpdatesSectionProps {
  /** Reads the build description, for its platform half. A thunk rather
   *  than the client, exactly as the About page takes its build facts — so
   *  the page can be driven from a stub. Absent in an embedding with no
   *  backend; the page then makes no platform claim. */
  load?: () => Promise<AppAbout>
}

export function UpdatesSection(props: UpdatesSectionProps) {
  const [platform] = createResource(() =>
    props.load ? props.load().then((a) => a.platform) : undefined,
  )

  return (
    <Show when={platform()?.startsWith('linux')}>
      <PageSection title="Installing updates on Linux">
        <StatusCard
          tone="warning"
          title="Linux updates"
          description="AppImage installs can update themselves in nocx. deb and rpm installs update through their package manager; that is a different install from the AppImage."
        />
      </PageSection>
    </Show>
  )
}
