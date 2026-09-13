//go:build !nocx_local_ssh

package app

// liveSshdDial is what the live-sshd fixture has to keep a DIALING build's
// state in, in the build that has none: nothing.
//
// The fixture is shared with this build — `live_sshd_carrier_test.go` is a
// `!nocx_local_ssh` file and `ssh_child_assembly_test.go` runs here too — and a
// struct's fields cannot carry a build tag, so the FIELD is shared and its type
// is not. This declaration is the coordinator's; live_sshd_dial_test.go holds
// the helper's, and exactly one of the two is ever compiled.
//
// Empty rather than a client that refuses to dial, for the reason
// internal/ssh's own dial_state_absent.go gives: a build this one may not make
// a connection in must have nowhere to PUT one. Nothing here reads it.
type liveSshdDial struct{}

// newLiveSshdDial answers the state this build is entitled to, which is none.
// It is called from the fixture's own constructor in both builds, so the shared
// field is written wherever it exists.
func newLiveSshdDial() liveSshdDial { return liveSshdDial{} }
