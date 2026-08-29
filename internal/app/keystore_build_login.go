//go:build nocx_login_session

package app

// THE DESKTOP HALF OF D10.
//
// `-tags nocx_login_session` is a build saying: this binary runs inside a
// login session, so there is a login keychain and there is a person who can
// answer a dialog about it. Only then may the startup probe run, and only
// then can the vault mint or read an OS-held key.
//
// It is a build property rather than an environment variable because the
// process it governs lives for days and any process of the user can set an
// environment variable (design §6). It is a tag rather than a platform check
// because the question is not "which OS" — it is "is anybody there", and a
// macOS binary run headlessly is exactly the case a platform check gets
// wrong.
const (
	buildKeystoreStance = keystoreReal
	buildKeystoreSource = "build (nocx_login_session)"
	buildKeystoreReason = "this build declares a login session, so the login keychain exists and somebody can answer for it"
)
