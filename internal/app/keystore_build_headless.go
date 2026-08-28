//go:build !nocx_login_session

package app

// THE HEADLESS HALF OF D10, and the default.
//
// A build without the nocx_login_session tag makes no claim to a login
// session, so it makes no claim to a keychain: the vault gets the file
// provider, the startup probe never runs, and the coordinator raises zero
// dialogs. That is the right default because the wrong answer is not
// symmetric. Guessing "there is a keychain" on a host without one is a modal
// nobody can dismiss in a process that lives for days; guessing "there is
// none" on a host with one is a vault that asks for its passphrase.
//
// A desktop build turns it on with `-tags nocx_login_session` — see
// keystore_build_login.go, which is the file that carries the other half of
// this constant.
const (
	buildKeystoreStance = keystoreAbsent
	buildKeystoreSource = "build (no nocx_login_session tag)"
	buildKeystoreReason = ""
)
