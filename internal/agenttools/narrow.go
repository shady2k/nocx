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

// narrowReadScreen is the readScreen row's capability constructor: the
// grant's ResourceSession scopes, nothing else. The renderer request seam —
// how a read is performed — is infrastructure wired at the run (the
// assistant's RendererRequester), not authority, so it does not live on the
// capability: the capability answers ONLY "may this run read this session",
// and an executor that holds it cannot name a session outside the grant
// (design §2.2: authority crosses in neither direction).
func narrowReadScreen(grant content.Grant) (Capability, error) {
	return NewScreenReader(grant.Scopes), nil
}

// narrowRun is the run row's capability constructor: the grant's
// ResourceSession scopes, nothing else — the same narrowing readScreen
// gets, as its own type (the middleware's InRenderer branch dispatches on
// the capability type, so the two tools stay distinguishable and the type
// switch is the exhaustiveness proof). The renderer request seam — how a
// run is submitted — is infrastructure wired at the run (the assistant's
// RendererRequester), not authority: the capability answers ONLY "may this
// run submit a command to this session", and an executor that holds it
// cannot name a session outside the grant.
func narrowRun(grant content.Grant) (Capability, error) {
	return NewRunner(grant.Scopes), nil
}
