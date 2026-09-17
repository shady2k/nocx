//go:build !nocx_local_ssh

package ssh

import "github.com/shady2k/nocx/internal/log"

// dialState is what a build without nocx_local_ssh has to keep a dialing
// client's state in: nothing.
//
// It is an empty struct and the constructor below answers nil, rather than a
// pool that would refuse to dial, because the point is that this build has
// nowhere to PUT a connection — a connection-shaped hole that always failed
// would be a dialing client with a lock on it. cmd/nocx-server is built
// without the tag and is the only caller of this declaration; the helper's
// build compiles dial_state_local.go instead (see RealClient's own doc, and
// plan 2026-09-13 §1, §3).
//
// Every method that would read the field lives in a tagged file, so there is
// nothing here a coordinator could call even if the type were not empty.
type dialState struct{}

// newDialState answers the state this build is entitled to, which is none.
func newDialState(log.Logger) *dialState { return nil }
