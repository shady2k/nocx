package storage

// shippedAppDirName is the profile directory the installed application owns:
// its settings, SSH profiles, vault documents and usage records. Only a build
// carrying `-tags release` resolves it — see appdir_release.go and its
// development counterpart.
//
// # Why the profile directory is chosen by the build
//
// Every backend nocx runs — the installed app, `wails dev`, `cmd/devharness`
// behind `make dev-web`, and the Playwright suite, which launches one of its
// own — used to resolve this same directory. A developer picking a theme and an
// e2e run that switches themes and "cleans up" by writing the default were
// therefore writing the same document, and the suite won last (nocx-ti8w).
//
// The isolation to prevent that already existed: e2e/harness.ts sets
// XDG_CONFIG_HOME for the backends the vault specs launch. It was correct, and
// it was applied to three specs out of twenty-five. That is the failure mode
// worth designing against — not an absent mechanism but a present one that
// somebody has to remember to apply, on every new spec, forever. Careful review
// does not catch it; a safe default does.
//
// So the split lives in the build rather than in a flag, an environment
// variable or a runner's setup code. There is nothing to pass and nothing to
// forget. termic reaches the same conclusion for the same reason
// (`APP_DIR = if cfg!(debug_assertions) { "termic_dev" } else { "termic" }`).
//
// # Why the release build is the tagged one
//
// The tag selects the shipped profile, so a build made without it lands in the
// development profile. The inverse would be a footgun of exactly the kind this
// is meant to close: one forgotten flag and a test run writes the user's real
// documents. Getting the tag wrong should cost a developer an empty profile,
// never a user their data.
//
// Release builds pass it in .github/workflows/release.yml; `make test` runs the
// suite a second time under the tag so the shipped side is compiled and
// asserted rather than assumed.
//
// # What this does not do
//
// It separates development from release. It does NOT separate two development
// backends from each other: `make dev-web` and a concurrent e2e run both land
// in the development profile and still overwrite each other's writes, because
// every DocumentStore consumer loads its document once and writes the whole
// thing back (nocx-h2gf). An explicit per-run state directory is the next step
// on nocx-ti8w and is what parallel e2e workers would need.
const shippedAppDirName = "nocx"
