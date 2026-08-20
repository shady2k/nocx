/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/app.about.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The app.about JSON-RPC result: what this build is (nocx-8bbp). Read-only and constant for the life of the process — three fields are written by the linker (-X, see internal/version/version.go for the exact paths) and three are read from the running process. It exists because a person filing a bug, or checking whether an update landed, previously had nothing to read and nothing to quote. Every field is required and every field is a non-empty string: a value the build does not carry is the word "unknown", never "", because a blank row on the About page cannot be told apart from a row that has not loaded. The renderer therefore never has to decide what to draw for a missing value, and never has to know the "dev" sentinel — `development` names that fact on the wire.
 */
export interface AppAbout {
  /**
   * The release number, matching the release manifest's `version` field so the two never need translating ("0.2.0"). An unstamped build reports "dev", and `development` is then true — read that flag rather than comparing this string to a sentinel.
   */
  version: string
  /**
   * The git sha the build came from, or "unknown".
   */
  commit: string
  /**
   * When the build was made, or "unknown". A stamped build writes RFC 3339; the field is not constrained to it, because the value is quoted into a bug report rather than parsed.
   */
  date: string
  /**
   * The toolchain that compiled the binary, as runtime.Version reports it ("go1.25.0").
   */
  go: string
  /**
   * The desktop shell's module version, taken from the build's own dependency list rather than restated as a constant, so it cannot disagree with go.mod. "unknown" when the binary carries no build info.
   */
  wails: string
  /**
   * GOOS/GOARCH ("darwin/arm64") — the pair that decides which release artifact this is.
   */
  platform: string
  /**
   * True when nothing stamped this build, so `version` is a placeholder rather than a release number. The updater keys the same fact off Version == "dev" (internal/version); this carries it to the renderer so no surface has to spell the sentinel a second time.
   */
  development: boolean
}
