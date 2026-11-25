import { t } from './i18n/locale-model'

/** Backend refusals are safe sentences; match only their stable text so a
 * wrapped path or platform errno never reaches the person. Detailed operation
 * failures use a remedy rather than leaking filesystem or verifier details. */
const REFUSALS = [
  'cannot install the update; allow writes to Applications and try again',
  'cannot find your home folder — check your account settings and try again',
  'the Applications folder is not writable — allow writes to Applications and try again',
  'the AppImage directory is not writable — move the AppImage to a directory you can write to, launch it there, and try again',
  'this is a development build; self-update is not available',
  'this is not a distributed AppImage build',
  'rollback: this was a first installation, with no previous target to restore',
  'the update install target is unavailable',
  'this window cannot quit the application',
  'state directory is not configured',
] as const

const FAILURES: readonly { pattern: RegExp; key: string; fallback: string }[] = [
  {
    pattern: /download|fetch/i,
    key: 'update.failure.download',
    fallback: 'The update could not be downloaded. Check your connection and try again.',
  },
  {
    pattern: /verif|checksum|signature|digest/i,
    key: 'update.failure.verify',
    fallback: 'The update could not be verified. Check for updates and download it again.',
  },
  {
    pattern: /extract|unpack|archive/i,
    key: 'update.failure.unpack',
    fallback: 'The update could not be unpacked. Check free space and try again.',
  },
  {
    pattern: /exchange|rename|permission|not writable|read.only/i,
    key: 'update.failure.replace',
    fallback: 'The update could not replace the app. Allow writes to the app’s folder and try again.',
  },
  {
    pattern: /not installed|install target|app bundle/i,
    key: 'update.failure.notInstalled',
    fallback:
      'This copy of nocx cannot be updated here. Install the latest release in a folder you can write to, then try again.',
  },
]

/**
 * Two refusals whose backend sentence is a STATEMENT rather than a remedy:
 * "self-update is not available" and "not a distributed AppImage build" say
 * what cannot happen and leave the person with nothing to do (review,
 * nocx-1ebrs.66.1.1). These keep the backend's own words and add the step.
 * They are matched before the plain refusals because both start with one.
 */
const REFUSAL_REMEDIES: readonly { match: string; key: string; fallback: string }[] = [
  {
    match: 'this is a development build; self-update is not available',
    key: 'update.failure.devBuild',
    fallback:
      'this is a development build; self-update is not available — run a release build, or use the package manager that installed nocx',
  },
  {
    match: 'this is not a distributed AppImage build',
    key: 'update.failure.appImage',
    fallback:
      'this is not a distributed AppImage build — download the AppImage from the releases page, or use the package manager that installed nocx',
  },
]

/**
 * The two names reconcile owns, and the only refusal that names an exact path
 * (nocx-vh2hr.1.14). A managed name holding somebody else's bundle stops every
 * update, and retrying changes nothing; the person needs the path to clear it.
 * The path is captured from its leading slash up to the fixed suffix — the
 * backend builds it from the install directory, and everything around it stays
 * the toast's own words, so no errno or plist detail reaches the person.
 */
const MANAGED_DEBRIS_PATH = /(\/[^\n]*?\.nocx-(?:swap|backup)\.app)/

const MANAGED_DEBRIS_REFUSAL = "is not nocx's own"

/** The managed bundle a refusal names, or null when it names none. ONE OWNER
 *  for the pattern: the toast names the path inside its remedy and the copied
 *  details list it as a field (nocx-vh2hr.1.16). */
export function managedDebrisPath(text: string): string | null {
  return MANAGED_DEBRIS_PATH.exec(text)?.[1] ?? null
}

export function updateFailureMessage(error: unknown): string {
  const text = error instanceof Error ? error.message : String(error)
  if (text.includes(MANAGED_DEBRIS_REFUSAL)) {
    const blocked = managedDebrisPath(text)
    if (blocked) {
      return t(
        'update.failure.blocked',
        'The update is blocked by {path}, which is not nocx’s own. Move or remove it, then try again.',
        { path: blocked },
      )
    }
  }
  const remedied = REFUSAL_REMEDIES.find((row) => text.includes(row.match))
  if (remedied) return t(remedied.key, remedied.fallback)
  const refusal = REFUSALS.find((message) => text.includes(message))
  if (refusal) return refusal
  const failure = FAILURES.find((row) => row.pattern.test(text))
  if (failure) return t(failure.key, failure.fallback)
  return t(
    'update.failure.generic',
    'The update could not finish. Try again, or install the latest release manually.',
  )
}
