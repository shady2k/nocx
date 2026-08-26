package agenttools

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

// narrowFilesRead is the files.read row's capability constructor (design §5's
// `Narrow: filesystem.ScopedReader`): a read-only view of exactly the grant's
// path scopes, over the local machine's provider. A grant without a path
// scope builds a capability that refuses every read (NewScopedReader with
// zero roots) — the tool can never exceed the grant because it never holds
// more than the grant's paths.
func narrowFilesRead(grant content.Grant) (Capability, error) {
	var paths []string
	for _, s := range grant.Scopes {
		if s.Kind == content.ResourcePath {
			paths = append(paths, s.ID)
		}
	}
	r, err := filesystem.NewScopedReader(context.Background(), local.New(), paths)
	if err != nil {
		return nil, fmt.Errorf("narrow files.read: %w", err)
	}
	return r, nil
}

// narrowSession is the session.list and session.read row's constructor:
// exactly the granted ResourceSession scopes. The session capability carries
// only the set of session ids; execution infrastructure is supplied by the
// assistant run and is not authority.
func narrowSession(grant content.Grant) (Capability, error) {
	return NewSessionReader(grant.Scopes), nil
}

// narrowRun is the run row's capability constructor: the grant's
// ResourceSession scopes, nothing else — the same session-scoped authority
// model used by session.read, as its own type (the middleware's execution
// dispatch distinguishes capabilities, and the type switch is the
// exhaustiveness proof). The renderer request seam — how a run is submitted
// — is infrastructure wired at the run (the assistant's RendererRequester),
// not authority: the capability answers ONLY "may this run submit a command
// to this session", and an executor that holds it cannot name a session
// outside the grant.
func narrowRun(grant content.Grant) (Capability, error) {
	return NewRunner(grant.Scopes), nil
}
