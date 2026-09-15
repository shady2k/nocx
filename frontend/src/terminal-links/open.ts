// ═══════════════════════════════════════════════════════════════════════════
// What a click DOES — the policy layer, above both surfaces and above the
// renderer (AD-6: the renderer reports facts; where a click lands is not its
// decision).
//
// Everything the outside world can do arrives as an injected dependency
// (AD-8), so this whole file is testable without a socket: `shell.openUrl`
// for a url (AD-1 — the renderer has no path to the Wails runtime, so "open
// on its hosting" goes through the control plane), `files.open` for a
// binding, and the file-viewer surface for the tab.
//
// ONE rule holds the file together: a click never ends in silence. Every
// refusal — no verified cwd, no home to expand `~` against, a session that
// has gone away — is a sentence the user reads. A link that quietly does
// nothing is indistinguishable from a broken app, and it is precisely the
// behaviour this feature was written to remove.
//
// OSC 8 is the same policy by another road: ADR-0029 says a program may ask
// and never chooses, so a hyperlink a program declared arrives here as a
// url target and is subject to exactly the rules above — nocx decides.
// ═══════════════════════════════════════════════════════════════════════════

import type { FilesOpenResult } from '../generated/files.open'
import type { ActiveOrigin } from '../pane-content'
import type { FileViewerTarget } from '../file-viewer'
import type { SessionHomeSource } from '../where/session-home'
import type { LinkTarget } from './detect'
import { resolvePath } from './resolve'

export type LinkPathProbe =
  | { kind: 'directory' }
  | { kind: 'file' }
  | { kind: 'absent' }
  | { kind: 'unknown'; reason: 'permission-denied' | 'unavailable' | 'other' }

/** The opener's entire window onto the app. */
// Every member is declared as a PROPERTY holding a function, never as a
// method — the opener stores this object and calls its members detached from
// it, so a `this`-dependent method would be called with the wrong receiver.
// open-url.ts states the same rule for the same reason.
export interface LinkOpenDeps {
  /** Hand a url to the system browser (shell.openUrl). */
  readonly openUrl: (url: string) => Promise<unknown>
  /** Classify a path with one files.stat call, without changing the Files
   *  panel. */
  readonly pathKind: (bindingId: string, path: string) => Promise<LinkPathProbe>
  /** Reveal a directory through the existing Files panel. Resolves true
   *  when the panel's store reaches an expandable directory, false when the
   *  path is a regular file or cannot be reached. */
  readonly openDirectory: (
    path: string,
    probe: Extract<LinkPathProbe, { kind: 'directory' }>,
  ) => Promise<boolean>
  /** Open (or focus) the file-viewer tab for one file. */
  readonly openViewer: (target: FileViewerTarget & { line?: number }) => void
  /** Tell the user why nothing opened. */
  readonly notify: (message: string) => void
  /** The session's one files.open binding and derived home (nocx-9bpeq.13,
   *  spec §3) — shared with the prompt line and any other where-consumer, so
   *  the opener never mints a binding of its own: a binding holds a provider
   *  and, for ssh, a pooled connection reference, and a second one per
   *  consumer would leak both at the rate consumers ask. The composition
   *  root builds it once and hands the same instance to every consumer. */
  readonly sessionHome: SessionHomeSource
}

export interface LinkOpener {
  readonly open: (target: LinkTarget, origin: Omit<ActiveOrigin, 'paneId'> | null) => Promise<void>
}

export function createLinkOpener(deps: LinkOpenDeps): LinkOpener {
  function bindingFor(origin: Omit<ActiveOrigin, 'paneId'>): Promise<FilesOpenResult> {
    return deps.sessionHome.ensure(origin.sessionId, origin.cwd, origin.cwdVerified)
  }

  async function openPath(
    target: Extract<LinkTarget, { kind: 'path' }>,
    origin: Omit<ActiveOrigin, 'paneId'>,
  ): Promise<void> {
    let binding: FilesOpenResult
    try {
      binding = await bindingFor(origin)
    } catch {
      deps.notify(`Could not reach this tab's filesystem to open ${target.path}`)
      return
    }
    const resolved = resolvePath(target, {
      cwd: origin.cwd ?? '',
      cwdVerified: origin.cwdVerified,
      home: deps.sessionHome.home(origin.sessionId),
    })
    if (!resolved.ok) {
      deps.notify(
        resolved.reason === 'no-home'
          ? `Could not work out the home directory, so ${target.path} could not be opened`
          : `This tab has not reported a working directory, so ${target.path} could not be resolved`,
      )
      return
    }
    const absolute = resolved.absolute
    let probe: LinkPathProbe = { kind: 'unknown', reason: 'unavailable' }
    try {
      probe = await deps.pathKind(binding.bindingId, absolute)
    } catch {
      // A rejected classifier is not proof that the path is a file. Keep the
      // refusal visible instead of handing an unclassified path to the viewer.
    }
    if (probe.kind === 'directory') {
      try {
        if (await deps.openDirectory(absolute, probe)) return
      } catch {
        // A failed reveal is not proof that the path is a file.
      }
      deps.notify(`Could not show directory ${target.path}`)
      return
    }
    if (probe.kind === 'absent') {
      deps.notify(`That path is not there: ${target.path}`)
      return
    }
    if (probe.kind === 'unknown') {
      deps.notify(
        probe.reason === 'permission-denied'
          ? `Could not inspect ${target.path}: permission was denied`
          : `Could not determine what ${target.path} is, so it was not opened`,
      )
      return
    }

    deps.openViewer({
      bindingId: binding.bindingId,
      endpointId: binding.endpointId,
      path: absolute,
      // The LEXICAL path stands in for the provider's canonical identity,
      // which is only known after a read. Two symlinks to one file can
      // therefore open two tabs when one is reached through each name — a
      // narrower gap than the double read that closing it would cost, and
      // the ordinary case (no symlink) dedups exactly.
      canonical: absolute,
      displayHost: origin.host,
      name: basename(absolute),
      origin,
      ...(target.line === undefined ? {} : { line: target.line }),
    })
  }

  return {
    async open(target, origin) {
      if (target.kind === 'url') {
        try {
          await deps.openUrl(target.url)
        } catch {
          deps.notify(`Could not open ${target.url}`)
        }
        return
      }
      if (origin === null) {
        deps.notify(`This tab is not attached to a filesystem, so ${target.path} cannot be opened`)
        return
      }
      await openPath(target, origin)
    },
  }
}

function basename(path: string): string {
  const cut = path.lastIndexOf('/')
  return cut === -1 ? path : path.slice(cut + 1)
}
