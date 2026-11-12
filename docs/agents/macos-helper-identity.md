# The local helper's macOS code identity

The local Darwin helpers are signed with the app's certificate and fixed
`dev.nocx.helper` identifier in `sign-local-helpers` in
`.github/workflows/release-build.yml` (PR #695). The job checks the unsigned
input hash, signs, verifies identity and republishes hashes of signed bytes.
Both app builds embed those exact artifacts. Remote helpers are unchanged.

Signing alone did not fix inherited responsible code: on 2026-11-12 the owner
measured a surviving helper with its own responsible PID but the app executable
as responsible path. Reparenting to launchd does not change that attribution.

[ADR-qy0pf1va](../decisions/qy0pf1va-the-darwin-helper-owns-its-responsible-code-from-spawn.md)
replaces this page's former no-disclaim instruction. The existing endpoint
start disclaims responsibility **once, for the helper**, not for pane shells.
The bare helper embeds a Local Network usage description before signing.
The app bundle's plist and grant do not transfer to it. It is not sandboxed,
so no network-client sandbox entitlement is needed or added.

The first gated LAN operation may prompt for `nocx-helper`; allow it, or enable
it in System Settings > Privacy & Security > Local Network if previously denied.
Exact presentation and grant persistence require the owner's Mac, not CI.

Acceptance needs two fixed nightlies. Open a pane in the first, update to the
second, then run Homebrew ssh to the same LAN host in both the old and a new
pane. Record shell continuity, helper generation, signature and responsible
path before and after. Apple ssh is an exemption control, not the pass probe.
Helpers already launched by pre-fix builds cannot acquire this new spawn
attribute retroactively; keep that bootstrap separate from acceptance.
