package agenttools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
)

// The effect ROW each general-purpose file tool's capability is bounded by.
// The row is where a person expressed which paths that class of call may
// touch (content.EffectPolicy: one row per effect, each carrying its own
// scopes), so the row — never the grant's derived fence union — is what
// filesystemRoots reads.
//
// These name the same effect as the tool's declaration row in registry.go.
// They are restated here rather than read back from `declarations` because
// that table's initializer names these constructors, and a constructor
// reading the table would be an initialization cycle;
// TestFileToolNarrowEffectsMatchDeclarations asserts the two agree, so the
// restatement cannot drift silently.
const (
	filesReadRowEffect  = content.EffectObserve
	filesWriteRowEffect = content.EffectMutateReversible
)

// The three file tools are built on THIS machine's filesystem (local.New()
// below), so a run whose pane is somewhere else must get no capability at all
// — otherwise an SSH pane reads and writes the local disk, which is the leak
// ADR-anntnxdj §5 closes until stage 8 gives these tools the pane's own
// provider through the files binding.
//
// Refusing HERE is the guarantee, not the answer: the constructor declining to
// construct is what makes "the tool cannot exceed what it was given" true by
// the same mechanism the grant's deadline uses (withinGrantLifetime). A caller
// that wants the model to READ a refusal composes a guard over the same two
// facts — the tool's name (ReachesLocalDisk) and the run's pane — so the
// sentence is ours rather than a failure about a constructor.
var ErrRemoteFileTools = errors.New("nocx's file tools reach this machine's disk only; the remote file tools are coming")

// ReachesLocalDisk reports whether the declaration's capability is this
// machine's filesystem: files.read, files.edit and files.create, whose
// constructors call local.New(). It is the one answer to "does this tool need
// the pane to be local", read by the constructors below and by the guard that
// turns their refusal into a sentence — so the two cannot disagree about which
// tools the fact is about. It goes with local.New() when the file tools move
// to the pane's own provider (ADR-anntnxdj stage 8).
func ReachesLocalDisk(name string) bool {
	switch name {
	case "files.read", "files.edit", "files.create":
		return true
	}
	return false
}

// localPaneOnly is why a file tool has no capability on a pane that is not
// this machine. The zero Environment names no pane and is refused with it: a
// caller that never said where the run is has not said it is here.
func localPaneOnly(tool string, runCtx RunContext) error {
	if runCtx.Environment.Kind == content.EnvLocal {
		return nil
	}
	return fmt.Errorf("%s: %w", tool, ErrRemoteFileTools)
}

// narrowFilesRead is the files.read row's capability constructor. It receives
// the resources resolved from this call and keeps only the path identities
// that are also in the grant; other grant paths are not authority for this
// call.
func narrowFilesRead(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
	if err := localPaneOnly("files.read", runCtx); err != nil {
		return nil, err
	}
	paths := resourceIDs(grant, resources, content.ResourcePath)
	r, err := filesystem.NewScopedReaderWithExactFiles(context.Background(), local.New(), filesystemRoots(grant, filesReadRowEffect), paths)
	if err != nil {
		return nil, fmt.Errorf("narrow files.read: %w", err)
	}
	return r, nil
}

// filesystemRoots is the containment scope the narrowed capability holds for
// one effect: the path ids of THAT effect row's scopes — the selector a
// person wrote, already materialized against the run fence at mint time
// (content.EffectPolicy.WithRunScopes).
//
// It is deliberately NOT grant.Scopes. That union is the run fence's
// DECLARATION coverage (see content.EffectPolicy.AsGrant), and since
// ADR-0028 decision 4's amendment of 2026-08-26 its path member is "/" for
// every run the product mints. Reading it therefore discarded whatever
// narrowing the operator expressed and handed the capability either the
// whole filesystem or — because a "/" root satisfied no containment test —
// nothing at all (nocx-cd6vp). Reading the row restores the narrowing, and
// it is what gives contained() something real to canonicalize against: a row
// scoped to one directory refuses a symlink out of it by canonical identity,
// which the lexical policy predicate cannot do.
func filesystemRoots(grant content.Grant, effect content.Effect) []string {
	scopes := grant.Policy.RowScopes(effect)
	roots := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if scope.Kind == content.ResourcePath {
			roots = append(roots, scope.ID)
		}
	}
	return roots
}

func narrowFilesEdit(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
	if err := localPaneOnly("files.edit", runCtx); err != nil {
		return nil, err
	}
	paths := resourceIDs(grant, resources, content.ResourcePath)
	editor, err := filesystem.NewScopedEditorWithExactFiles(context.Background(), local.New(), filesystemRoots(grant, filesWriteRowEffect), paths)
	if err != nil {
		return nil, fmt.Errorf("narrow files.edit: %w", err)
	}
	return editor, nil
}

func narrowFilesCreate(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
	if err := localPaneOnly("files.create", runCtx); err != nil {
		return nil, err
	}
	// A new target cannot itself be canonicalized until it exists. Bind the
	// capability to the existing parent directories of the resolved targets;
	// ScopedEditor.Create canonicalizes that parent before writing.
	paths := resourceIDs(grant, resources, content.ResourcePath)
	parents := make([]string, 0, len(paths))
	for _, path := range paths {
		parents = append(parents, filepath.Dir(path))
	}
	editor, err := filesystem.NewScopedEditorWithExactParents(context.Background(), local.New(), filesystemRoots(grant, filesWriteRowEffect), parents)
	if err != nil {
		return nil, fmt.Errorf("narrow files.create: %w", err)
	}
	return editor, nil
}

// narrowSession is the session.list and session.read constructor. The
// capability carries only the resolved session identities that the grant also
// permits.
func narrowSession(grant content.Grant, resources []ResourceRef, runCtx RunContext) (Capability, error) {
	scoped := grantedResources(grant, resources)
	scopes := make([]content.GrantScope, 0, len(scoped))
	for _, ref := range scoped {
		if ref.Kind == content.ResourceSession {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return NewSessionReader(scopes, runCtx.AutomaticSessionItems, runCtx.MarkedSessionWindows), nil
}

// narrowURL carries the grant's destination ENDPOINTS, not this call's
// resolved URLs: a redirect hop lands on a URL the call never named and is
// judged against the same endpoint the page shows. Only endpoints that
// actually cover a resolved destination are carried, so an endpoint this
// call never reached stays out of the capability.
func narrowURL(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	return &URLScope{Endpoints: destinationEndpoints(grant.Scopes, resources)}, nil
}

// destinationEndpoints keeps the destination scopes of `scopes` that cover a
// destination this call actually resolved — the grant's own endpoints, whole,
// with the subdomain marker still on them.
//
// A second selection written beside this one would agree with it until the
// day somebody taught one of them a new kind of endpoint — which is exactly
// the drift the endpoint marker was added to survive (nocx-5nkm7), since
// rebuilding a scope from its kind and id silently drops it.
//
// Bounded by the call in the way narrowSession is: an endpoint of the grant
// that covered nothing this call resolved is not carried, so one grant naming
// two endpoints cannot let a call reach the one it never asked for.
func destinationEndpoints(scopes []content.GrantScope, resources []ResourceRef) []content.GrantScope {
	endpoints := make([]content.GrantScope, 0, len(scopes))
	for _, scope := range scopes {
		if scope.Kind != content.ResourceDestination {
			continue
		}
		for _, ref := range resources {
			if ref.Kind != content.ResourceDestination {
				continue
			}
			if scope.Contains(content.GrantScope{Kind: ref.Kind, ID: ref.ID}) {
				endpoints = append(endpoints, scope)
				break
			}
		}
	}
	return endpoints
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

// narrowRunWait is the session.wait row's capability constructor. Same
// authority as narrowRun — the right to keep waiting on a command travels
// with the right to have started it — in the capability type the renderer
// dispatch switch distinguishes it by.
func narrowRunWait(grant content.Grant, resources []ResourceRef, _ RunContext) (Capability, error) {
	scoped := grantedResources(grant, resources)
	scopes := make([]content.GrantScope, 0, len(scoped))
	for _, ref := range scoped {
		if ref.Kind == content.ResourceSession {
			scopes = append(scopes, content.GrantScope{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return NewRunWatcher(scopes), nil
}
