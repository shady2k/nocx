# The local helper's macOS code identity

This page exists because a signed helper is a **release property**, not a code
path: nothing in the Go sources changes when it is right, and a regression is
invisible until a person updates an installed app and a pre-update tab loses
the local network (nocx-vh2hr.1.7).

## What is signed

The two darwin variants of the **local** helper —
`internal/helper/deploy/artifacts/bin/local/nocx-helper-darwin-{amd64,arm64}.gz`
— are signed in the `sign-local-helpers` job of
`.github/workflows/release-build.yml` with:

- the app's certificate (`MACOS_SIGNING_P12`, CN `nocx Project Code Signing`),
- the fixed identifier `dev.nocx.helper`,
- no responsibility-disclaim call anywhere in the spawn chain.

The deployable (remote) helpers are not signed: they run on hosts this product
does not own, and macOS Local Network Privacy is not their question.

The job takes the bytes the Linux `helpers` job built, checks each decompressed
helper against that job's published hash, signs it, runs
`cmd/check-helper-signature` (identifier, authority, never ad-hoc), and
republishes the local artifact plus the hashes of the **signed** bytes. Both
build jobs embed that artifact and `sign` compares their
`embedded-local-hashes.txt` against it, so the chain of custody is
`helpers job -> signing job's input check -> signed bytes`.

## Why the signature matters (Apple's rules)

TN3179, _Understanding local network privacy_, macOS considerations:

- macOS attributes a process's local-network operation to its **responsible
  code**; for "an app [that] spawns a helper tool", macOS "considers the app to
  be the responsible code" and records the person's choice "for the whole app,
  not just that specific helper tool".
- A standalone executable normally needs no usage description because it is
  either automatically allowed (a launchd daemon, a program running as root, a
  command-line tool run from Terminal or SSH) or "ha[s] an app that acts as
  their responsible code, for example, a helper tool".
- Local network privacy "tracks the identity of your program using its code
  signature"; ad-hoc signed code is exactly the case it tracks badly.

The nocx helper is none of the automatically-allowed cases: it is started by
`nocx-server`, not by launchd, and it is not run from Terminal. While the app
exists the app is its responsible code. After an in-place replacement the old
helper outlives the app (ADR-0104 keeps helpers and their shells alive), is
reparented to launchd, and is the nearest surviving process — so its own code
identity is what macOS has left to match. An ad-hoc helper's identifier comes
from the link output's file name (`a.out` in the owner's evidence), which makes
every build a different program and leaves no grant to match.

## What the signature does, and does not, carry

The signature gives the helper a **stable designated requirement**. A Local
Network decision recorded for `dev.nocx.helper` with this certificate survives
every later build, which is the property the ad-hoc identity could not have.

It does **not** transfer the app's existing grant, because the identifier is
part of the code identity (`dev.nocx` is a different program to LNP). Keeping
the app as the responsible code after it has been replaced is also impossible
through any public API: responsibility is inherited when a child is spawned,
the app process quits during the update, and the only private lever — a
responsibility disclaim at the helper — is forbidden by the owner's decision
and would make every pane shell its own program instead. So the durable answer
is the helper's own stable identity, and the first local-network operation
from a pre-update tab after this build may present one local-network alert
attributed to the helper. Accepting it records the grant; later builds keep it.

Residual risk, stated rather than hidden: the helper is a bare executable with
no `Info.plist`, so it carries no `NSLocalNetworkUsageDescription`. TN3179 says
a standalone executable that needs that property should embed an `Info.plist`
section. If the check below still fails with no alert, the next step is that
embedding (or signing the helper with the app's `dev.nocx` identifier to reuse
the app's grant) — not another identity change.

## The bootstrap: two updates, not one

The helper that survives an update is the one the **previous** build installed.
So the update from a build without this fix to the first build with it still
leaves the old ad-hoc helper in front of the old tab; the fix can only be
observed on the update **after** the signed helper was installed.

## What the owner checks

After installing the signed nightly and opening at least one tab (which installs
the signed helper), take the next in-app update, then in a tab opened **before**
that update run:

```sh
/opt/homebrew/bin/ssh -o ConnectTimeout=5 -o BatchMode=yes <host> true; echo $?
```

`0` is the pass. A `No route to host` with the diagnostic's chain still naming
an ad-hoc helper means the pre-update tab is still served by the old generation
(the bootstrap above), not that the signature is wrong.
