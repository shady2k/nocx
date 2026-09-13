package app

import (
	"context"

	"github.com/shady2k/nocx/internal/completion"
	"github.com/shady2k/nocx/internal/ssh"
)

// routedSSHCompleter binds the completion engine to this machine's helper
// without making internal/completion import the helper client. Every call
// receives the exact options captured from the live session, which is what makes
// a jump route resolve to the connection the session itself reached.
//
// There is no adapter between the two any more and that is the change the probe
// seam took: completion names the probe and passes its typed arguments, and
// `helperProbes` answers it with a lease on the helper's pooled connection —
// the same shape completion's own seam already had, one process out.
type routedSSHCompleter struct {
	probes *helperProbes
}

func (c *routedSSHCompleter) Complete(ctx context.Context, req completion.Request, opts ...ssh.ConnectOption) (*completion.Response, error) {
	return completion.NewSSH(c.probes.HelperCompletionProvider(opts...)).Complete(ctx, req)
}
