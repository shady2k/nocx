package app

import (
	"context"

	"github.com/shady2k/nocx/internal/pty"
)

// reachPTYFactory hands every pane the same stub PTY. It is untagged because
// stands in both build states use it: the live launcher-reachability stand
// (nocx_local_ssh) and the ssh-pane screen-owner stand (default build).
type reachPTYFactory struct{ stub *pty.Stub }

func (f *reachPTYFactory) NewPTY(_ context.Context, _ pty.Config) (pty.Pty, error) {
	return f.stub, nil
}
