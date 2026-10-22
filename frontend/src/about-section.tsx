/**
 * AboutSection — the Settings page that says what build this is (nocx-8bbp).
 *
 * The settings rail had Clipboard, Interface, Export/Backup/Import and
 * Connections, and nothing said what build the app was. A person filing a bug,
 * or checking whether an update landed, had nothing to read and nothing to
 * quote. That is the whole design brief, and it is why the copy affordance is
 * not a nicety: the reason anybody opens this page is to paste it somewhere.
 *
 * Every value comes off the wire. Nothing here is derived, defaulted or
 * guessed — including the "development build" mark, which reads the wire's own
 * `development` flag rather than comparing the version to a sentinel this
 * surface would then be the second owner of.
 */

import { For, Show, createResource, createSignal, type Accessor } from 'solid-js'

import { Badge, Button, PageSection, Stack, StatusCard } from './ui'
import { showToast } from './ui/toast'
import type { AppAbout } from './generated/app.about'
import type { ClipboardAccess } from './clipboard'
import type { UpdateCheckOutcome } from './update-check'

/** The application's own icon, served from `public/` and therefore addressed
 *  as a URL rather than imported as a module. Vite copies that directory into
 *  dist verbatim, so the path is the same in the dev stand and in the packaged
 *  app, and TypeScript is not asked to have an opinion about a PNG. Relative,
 *  because the built app is loaded with `base: './'` and never navigates away
 *  from its one document. */
const APP_ICON = './appicon-96.png'

export interface AboutSectionProps {
  /** Reads the build description. A thunk rather than the client itself, so
   *  the page can be driven from a stub without one existing. */
  load: () => Promise<AppAbout>
  clipboard: ClipboardAccess
  /** Why the last automatic update check failed, or null when it succeeded
   *  or has not run (nocx-1ebrs.36). Absent in an embedding with no window
   *  updater; the page then makes no claim either way. An accessor rather
   *  than a string, because the 24-hour re-check can clear it after the
   *  page is open. */
  updateCheckError?: Accessor<string | null>
  /** Runs one update check through the shared createUpdateCheck owner and
   *  resolves what it found, or null when it failed (nocx-1ebrs.55). The
   *  controller is injected rather than imported: the seam a test substitutes
   *  is the seam a dev stand can leave out, and the page then offers no
   *  button rather than a dead one. */
  onCheckForUpdates?: () => Promise<UpdateCheckOutcome | null>
}

/** Every fact this page knows, in the order a bug report wants them: what it
 *  is, then what it was built from. The labels are the surface's; the values
 *  are the wire's.
 *
 *  ONE LIST, TWO PROJECTIONS. The version is also drawn large beside the icon,
 *  because that is the thing somebody actually came to read — so it is marked
 *  `headline` and the list below leaves it out rather than printing it twice.
 *  The clipboard takes the whole list including it. Two hand-kept lists would
 *  have been the obvious shape and would have drifted the first time a field
 *  was added to one of them. */
const FACTS: { label: string; of: (a: AppAbout) => string; headline?: true }[] = [
  { label: 'Version', of: (a) => a.version, headline: true },
  { label: 'Commit', of: (a) => a.commit },
  { label: 'Built', of: (a) => a.date },
  { label: 'Platform', of: (a) => a.platform },
  { label: 'Go', of: (a) => a.go },
  { label: 'Wails', of: (a) => a.wails },
]

/** The facts the list draws: everything the headline has not already said. */
const ROWS = FACTS.filter((f) => !f.headline)

/** What lands on the clipboard: the whole block, one line per row, in the
 *  order it is on screen. Plain text rather than a fenced block — it is pasted
 *  into issue trackers, chat and mail alike, and only one of those renders
 *  markdown. */
function diagnosticsText(about: AppAbout): string {
  return ['nocx', ...FACTS.map((fact) => `${fact.label}: ${fact.of(about)}`)].join('\n')
}

export function AboutSection(props: AboutSectionProps) {
  const [about] = createResource(() => props.load())
  // Checking is this page's own state: the shared controller runs checks
  // nobody pressed a button for (startup, the interval), and only the check
  // THIS button started belongs on the button.
  const [checking, setChecking] = createSignal(false)
  // The answer to the last manual check that found nothing newer. An
  // available build is not kept here — the update toast announces it
  // (nocx-1ebrs.54), and a second wording of it on this page would be a
  // second owner of one announcement.
  const [upToDate, setUpToDate] = createSignal<string | null>(null)

  async function checkForUpdates(): Promise<void> {
    if (checking()) return
    setChecking(true)
    try {
      const outcome = await props.onCheckForUpdates?.()
      if (outcome?.kind === 'up-to-date') {
        setUpToDate(`You're up to date (${about()?.version ?? ''}, channel ${outcome.channel})`)
      } else {
        // An available build is the toast's to say; a failure is the error
        // card's, with its cause. Neither is a quiet "up to date".
        setUpToDate(null)
      }
    } finally {
      setChecking(false)
    }
  }

  async function copy(): Promise<void> {
    const value = about()
    if (!value) return
    try {
      await props.clipboard.writeText(diagnosticsText(value))
      showToast({ message: 'Build details copied' })
    } catch (err) {
      // The clipboard is a platform capability that can refuse — a non-secure
      // context, a platform that declined the write. A button that silently
      // does nothing is worse than one that is not there.
      showToast({
        message: `Could not copy: ${err instanceof Error ? err.message : String(err)}`,
        level: 'danger',
      })
    }
  }

  return (
    <PageSection title="About">
      <Show
        when={!about.error}
        fallback={
          <StatusCard
            tone="danger"
            title="Could not read this build's details"
            description="The backend answered with an error."
          />
        }
      >
        <Show when={about()} fallback={<p class="ab-loading">Reading the build…</p>}>
          {(build) => (
            <Stack gap="loose">
              {/* A soft degrade is visible in the product, not only in a log:
                  this is the surface a person reads when they wonder why no
                  update was offered. The state is live — the accessor, not a
                  snapshot — so a re-check that succeeds clears the card. */}
              <Show when={props.updateCheckError?.() ?? null}>
                {(reason) => (
                  <StatusCard
                    tone="warning"
                    title="Updates"
                    description={`update check failed: ${reason()}`}
                  />
                )}
              </Show>
              <div class="ab-identity">
                {/* Decorative: the name is beside it in text, so an alt would
                    be read out twice by a screen reader. */}
                <img class="ab-icon" src={APP_ICON} alt="" width="72" height="72" />
                <div class="ab-identity__text">
                  <p class="ab-name">nocx</p>
                  <p class="ab-version">{build().version}</p>
                  <Show when={build().development}>
                    {/* "dev" is a placeholder, not a release number, and the
                        question this page is asked is "is this the build with
                        the fix in it". Saying so is the answer; printing "dev"
                        where a version goes is not. */}
                    <Badge tone="warning">Development build</Badge>
                  </Show>
                </div>
              </div>

              {/* A description list rather than the kit's Field: these are read
                  facts, not form rows, and a horizontal Field is a
                  <label for=…> pointing at a control that does not exist here.
                  The kit owns controls; this is content, and dt/dd is what
                  content of this shape is. */}
              <dl class="ab-rows">
                <For each={ROWS}>
                  {(row) => (
                    <>
                      <dt>{row.label}</dt>
                      <dd>{row.of(build())}</dd>
                    </>
                  )}
                </For>
              </dl>

              <div class="ab-actions">
                <Button onClick={() => void copy()}>Copy diagnostics</Button>
                <Show when={props.onCheckForUpdates}>
                  <Button onClick={() => void checkForUpdates()} disabled={checking()}>
                    {checking() ? 'Checking…' : 'Check for updates'}
                  </Button>
                </Show>
              </div>
              {/* The manual check's answer, said where the button that asked
                  for it lives. Only the up-to-date case: an available build
                  is announced by the update toast, and a failure by the
                  warning card above with its cause. */}
              <Show when={upToDate()}>{(line) => <p class="ab-check-result">{line()}</p>}</Show>
            </Stack>
          )}
        </Show>
      </Show>
    </PageSection>
  )
}
