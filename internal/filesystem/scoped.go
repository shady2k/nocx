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
// the grant's path scopes, held by the tool INSTEAD of the provider, so the
// tool cannot exceed the grant because it never holds more. Go package
// privacy is not the boundary; this is.
//
// The scope is the provider-canonical identity of each allowed root,
// resolved at construction: a path that resolves (through symlinks, which
// Canonical follows) inside a root is inside the scope, and a symlink into
// the scope from outside is still inside — the identity is what counts.
// Nothing in this package interprets a path (spec §5.2: path syntax belongs
// to the provider); the containment check compares provider-canonical
// strings at a separator boundary.
type ScopedReader struct {
	provider Provider
	roots    []string // canonical absolute roots; empty = nothing is in scope
}

// ErrOutOfScope is the refusal a read outside the scope returns. It is a
// capability fact, not a policy verdict: the policy may have decided to ask
// about such a call (scope expansion escalates); the capability refuses
// regardless, because it cannot express the call at all.
var ErrOutOfScope = errors.New("filesystem: path outside the grant's scope")

// ScopedContent is one bounded read result plus the window contract's total:
// the file's full size when known (Content.Size is bytes RETURNED, which is
// the window, not the file — design §4.4 needs both).
type ScopedContent struct {
	Content
	Total int64
}

// NewScopedReader builds the capability over p for exactly the given roots.
// A root that cannot be canonicalized is an error at construction — a scope
// whose identity is unknowable must not silently become a wider or narrower
// scope. Zero roots build a capability that refuses every read.
func NewScopedReader(ctx context.Context, p Provider, roots []string) (*ScopedReader, error) {
	s := &ScopedReader{provider: p}
	for _, r := range roots {
		c, err := p.Canonical(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("filesystem: scope root %q: %w", r, err)
		}
		s.roots = append(s.roots, c)
	}
	return s, nil
}

// Read returns the windowed content of path if and only if the path's
// canonical identity is inside the scope, and refuses otherwise — before the
// provider is touched. maxBytes is passed through to the provider's own
// ceiling.
func (s *ScopedReader) Read(ctx context.Context, path string, maxBytes int64) (ScopedContent, error) {
	canonical, err := s.provider.Canonical(ctx, path)
	if err != nil {
		return ScopedContent{}, err
	}
	if !contained(canonical, s.roots) {
		return ScopedContent{}, fmt.Errorf("%w: %s", ErrOutOfScope, path)
	}
	c, err := s.provider.Read(ctx, path, maxBytes)
	if err != nil {
		return ScopedContent{}, err
	}
	total := c.Size
	if c.Truncated {
		// The provider read only the window, so the file's full size needs
		// its own query. The local provider's canonical path is directly
		// stat-able; a non-local provider will need its own size source when
		// a scoped reader is built over it (later slice — today the reader
		// is local-only).
		if st, err := os.Stat(canonical); err == nil {
			total = st.Size()
		}
	}
	return ScopedContent{Content: c, Total: total}, nil
}

// contained reports whether canonical is one of the roots or a descendant at
// a separator boundary. The separator set covers the provider-native forms:
// "/" (POSIX local, SFTP) and the OS separator where it differs (Windows
// local).
func contained(canonical string, roots []string) bool {
	for _, root := range roots {
		if canonical == root {
			return true
		}
		for _, sep := range scopeSeparators() {
			if strings.HasPrefix(canonical, root+string(sep)) {
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
