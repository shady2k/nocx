package agenttools

import (
	"context"
	"fmt"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

// narrowFilesRead is the files.read row's capability constructor. It receives
// the resources resolved from this call and keeps only the path identities
// that are also in the grant; other grant paths are not authority for this
// call.
func narrowFilesRead(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	paths := resourceIDs(grant, resources, content.ResourcePath)
	r, err := filesystem.NewScopedReader(context.Background(), local.New(), paths)
	if err != nil {
		return nil, fmt.Errorf("narrow files.read: %w", err)
	}
	return r, nil
}

// narrowSession is the session.list and session.read constructor. The
// capability carries only the resolved session identities that the grant also
// permits.
func narrowSession(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	scoped := grantedResources(grant, resources)
	scopes := make([]content.GrantScope, 0, len(scoped))
	for _, ref := range scoped {
		if ref.Kind == content.ResourceSession {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return NewSessionReader(scopes), nil
}

// narrowRun is the run row's capability constructor. It carries only the
// resolved session identities that the grant also permits.
func narrowRun(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	scoped := grantedResources(grant, resources)
	scopes := make([]content.GrantScope, 0, len(scoped))
	for _, ref := range scoped {
		if ref.Kind == content.ResourceSession {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return NewRunner(scopes), nil
}
