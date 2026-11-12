# ADR-qy0pf1va — The Darwin helper owns its responsible code from spawn

- **Status:** Accepted (owner direction, 2026-11-12; `nocx-vh2hr.1.26`)
- **Date:** 2026-11-12
- **Refines:** ADR-0104's surviving helper lifecycle; no session migration.

## Context

The owner's Mac showed Homebrew OpenSSH failing a LAN connect only in a pane
opened before an in-app update. Apple ssh and nc succeeded there; Homebrew ssh
succeeded in Terminal and in a new nocx pane. Apple platform binaries are exempt
from Local Network privacy. `launchctl procinfo` showed the surviving helper's
own PID as responsible PID, but `/Applications/nocx.app/Contents/MacOS/nocx`
as responsible path. That path was replaced by the update; the helper's
content-addressed binary was untouched. This establishes inherited responsible
code, not the exact internal privacy database rejection or signature comparison.

`local.reach` calls `endpoint.Ensure`, whose existing start boundary directly
executes the installed helper with `serve`. There is no app re-exec and no
launchd job. `Setsid` detaches a Unix session, not a macOS responsibility record.
Reparenting to PID 1 does not change that record. Signing the helper alone
(nocx-vh2hr.1.7) therefore did not solve this case.

AD-2 keeps one core, AD-7 keeps session identity authoritative at its existing
owner, and AD-8 puts this change at the existing endpoint start boundary.
AD-1 and AD-6 are unchanged: no terminal-byte inspection or connection-error
heuristic is added. ADR-0104 still keeps running helpers and shells alive.

## Decision

On Darwin only, the existing endpoint start uses `posix_spawn` with
`responsibility_spawnattrs_setdisclaim(attr, 1)` before exec. This private
libSystem API is resolved with `dlsym` because the Linux cross compiler's SDK
need not export a private symbol. Missing API or spawn failure refuses the
start; there is no undisclaimed fallback. The helper, not each pane shell,
starts a new responsibility record. Its descendants inherit it.

Keep environment inheritance, detached session, close-on-exec descriptors,
`/dev/null` standard streams, child reaping and the existing endpoint/exited
wait and yield semantics. Cancellation still does not kill a legitimate daemon.
Non-Darwin spawn remains `exec.Command` plus the existing detach attributes.
There is no second launcher or app re-exec. The Darwin coordinator now builds
with cgo and explicit architecture flags; this links libSystem, not Wails or
GTK. A cgo-disabled Darwin build fails rather than producing an unusable server.
Linux coordinator builds remain cgo-disabled.

The local helper retains the fixed `dev.nocx.helper` identifier and the app's
signing certificate from PR #695. Before signing, its bare Mach-O embeds
`__TEXT,__info_plist` with that identifier, name and
`NSLocalNetworkUsageDescription`. The app bundle's usage description does not
apply to this separate responsible executable. No sandbox entitlement is added:
the helper is not App Sandbox confined; the network-client sandbox entitlement
is not a Local Network privacy grant.

This supersedes the no-disclaim instruction and automatic-reparenting claim in
`docs/agents/macos-helper-identity.md`, not an accepted ADR. The owner explicitly
requested the new spawn boundary after measuring responsible path. Existing
accepted records are not edited.

## Consequences and proof

The first gated LAN operation may show a macOS Local Network prompt naming
`nocx-helper`, separate from nocx's grant. Allow it; if denied, enable it in
System Settings > Privacy & Security > Local Network. Exact prompt presentation
and grant persistence still require the owner's Mac. Stable signing is necessary
but is not itself proof of a grant. No automatic repair of existing pre-fix
helpers is possible without ending the sessions this feature preserves.

Darwin-only tests selected automatically by ci-mac launch the real helper via
`endpoint.Ensure`, query its peer PID, and assert `launchctl procinfo`'s
**responsible path**, not just responsible PID, equals the helper executable.
The CI runner needs passwordless sudo for procinfo; an unavailable query fails,
not skips. Another Darwin test reads the built helper's embedded plist.
Linux continues exercising the existing endpoint suite.

Final acceptance is on the owner's Mac across **two fixed nightlies**: open a
pane under the first, update to the second, and prove the same shell still runs
Homebrew ssh to the same LAN host successfully, alongside a new pane. Record
app builds, helper generations, responsible paths, signatures and old-shell
continuity before and after. Apple ssh is only an exemption control. A CI TCP
connection or this Linux implementation report is not that acceptance.
