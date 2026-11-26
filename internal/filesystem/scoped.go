package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ScopedReader is the narrowed capability a files.read grant hands its tool
// (design §5's `Narrow: filesystem.ScopedReader`; ADR-0028 decision 4 — the
// dispatcher narrows, it does not check). It is a read-only view of exactly
// the grant's path scopes, held by the tool INSTEAD of a provider, so
// the tool cannot exceed the grant because it never holds more. Go package
// privacy is not the boundary; this is.
//
// It holds a HandleSource and NOT a provider, and that is the stage-10 rule
// (ADR-anntnxdj §5): every operation acquires a short-lived Handle from the
// pane's own binding, so the same capability reads this machine's disk on a
// local pane and the host's files on an SSH pane, and it can tell neither
// from the other. Nothing is resolved at construction — a capability is built
// with no I/O at all, which is what lets a run be admitted before anybody has
// opened the Files panel.
//
// The scope is the provider-canonical identity of each allowed root, resolved
// at the START of each operation on the handle that operation holds: a path
// that resolves (through symlinks, which Canonical follows) inside a root is
// inside the scope, and a symlink into the scope from outside is still inside
// — the identity is what counts. Nothing in this package interprets a path
// (spec §5.2: path syntax belongs to the provider); the containment check
// compares provider-canonical strings at a separator boundary.
type ScopedReader struct {
	source     HandleSource
	roots      []string // spelled scope roots; canonicalized per operation
	exactFiles []string // spelled exact-file identities; canonicalized per operation
	exactOnly  bool     // exact files further narrow the canonical grant roots
}

// ErrOutOfScope is the refusal a read outside the scope returns. It is a
// capability fact, not a policy verdict: the policy may have decided to ask
// about such a call (scope expansion escalates); the capability refuses
// regardless, because it cannot express the call at all.
var ErrOutOfScope = errors.New("filesystem: path outside the grant's scope")

// NewScopedReaderWithExactFiles retains the spelled grant roots and exact file
// identities for containment; both are canonicalized inside the operation,
// over the handle that operation acquired. This is the files.read capability
// shape: a file cannot itself be a directory root, because a root would
// authorize descendants, so exact files are a separate scope kind. Empty files
// therefore authorize no reads.
func NewScopedReaderWithExactFiles(source HandleSource, roots, files []string) *ScopedReader {
	if source == nil {
		return nil
	}
	return &ScopedReader{
		source:     source,
		roots:      append([]string(nil), roots...),
		exactFiles: append([]string(nil), files...),
		exactOnly:  true,
	}
}

// acquire takes one short-lived handle and canonicalizes the scope on it. Every
// operation starts here, and the caller must defer the returned release on
// every path after a nil error.
func (s *ScopedReader) acquire(ctx context.Context) (Handle, func(), []string, []string, error) {
	h, release, err := s.source.Acquire(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	roots, err := canonicalAll(ctx, h, s.roots)
	if err != nil {
		release()
		return nil, nil, nil, nil, fmt.Errorf("filesystem: scope root: %w", err)
	}
	files, err := canonicalAll(ctx, h, s.exactFiles)
	if err != nil {
		release()
		return nil, nil, nil, nil, fmt.Errorf("filesystem: exact file: %w", err)
	}
	return h, release, roots, files, nil
}

// canonicalAll resolves every spelled path through the handle. Empty input is
// an empty output and costs nothing.
func canonicalAll(ctx context.Context, h Handle, paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		c, err := h.Canonical(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", p, err)
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *ScopedReader) allows(canonical string, roots, files []string) bool {
	if s.exactOnly {
		return exact(canonical, files) && contained(canonical, roots)
	}
	return contained(canonical, roots)
}

func exact(canonical string, files []string) bool {
	for _, file := range files {
		if canonical == file {
			return true
		}
	}
	return false
}

// contained reports whether canonical is one of the roots or a descendant at
// a separator boundary. The separator set covers the provider-native forms:
// "/" (POSIX local, SFTP) and the OS separator where it differs (Windows
// local).
//
// A root that IS a separator — the filesystem root "/" — is the one root the
// boundary test cannot express by appending one: root+sep is "//", which no
// canonical path has, so such a root contained nothing but itself. That is
// not a corner case in this product. ADR-0028 decision 4, as amended
// 2026-08-26, makes "/" the run grant's path fence for EVERY run, so the
// unsatisfiable check made files.read, files.edit and files.create refuse
// every file in the shipped app (nocx-cd6vp).
//
// Accepting "/" does not weaken the check into the lexical predicate
// content.pathScopeContains is: containment here still compares
// PROVIDER-CANONICAL identities, so a symlink out of a root is still outside
// it. What a "/" root legitimately contains is every path the provider could
// canonicalize, which is exactly what the amendment says a run fence of "/"
// means. The narrowing a person expressed lives in the effect ROW's scopes,
// and agenttools.filesystemRoots builds these roots from that row — so a
// scoped row still refuses a symlink escape by canonical identity.
func contained(canonical string, roots []string) bool {
	for _, root := range roots {
		if canonical == root {
			return true
		}
		for _, sep := range scopeSeparators() {
			s := string(sep)
			if root == s {
				// The root is the separator itself: every path spelled from
				// it is a descendant, and there is no second separator to
				// test at the boundary.
				if strings.HasPrefix(canonical, s) {
					return true
				}
				continue
			}
			if strings.HasPrefix(canonical, root+s) {
				return true
			}
		}
	}
	return false
}

func scopeSeparators() []byte {
	if os.PathSeparator == '/' {
		return []byte{'/'}
	}
	return []byte{byte(os.PathSeparator), '/'}
}
