//go:build nocx_local_ssh

package ssh

import "github.com/shady2k/nocx/internal/log"

// dialState is the state a DIALING RealClient has: the connection pool AD-4
// requires, which every dial in this package goes through so that one
// principal reaching one endpoint over one route authenticates once and shares
// it.
//
// It is a type rather than three fields on RealClient because RealClient
// exists in both builds and a struct's fields cannot be selected by a build
// tag: the coordinator constructs this client, resolves through it and answers
// host-key verdicts with it (ssh_real.go), and it may not hold a connection.
// So the FIELD is shared and its type is not — dial_state_absent.go declares
// the empty half, and exactly one of the two files is ever compiled.
//
// Nothing outside the tagged files touches it (ssh_real_dial.go, pool.go,
// ssh_dial.go, ssh_channel.go, ssh_pooled.go).
type dialState struct {
	pool *ConnPool
}

// newDialState answers the state this build is entitled to: the pool. It is
// per-client because the pool is: one pool serves every host one daemon hosts,
// for that daemon's whole life, which is the same scope as the client that
// owns it.
func newDialState(logger log.Logger) *dialState {
	return &dialState{pool: NewConnPool(logger)}
}
