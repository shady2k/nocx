package capability

import (
	"context"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// ProviderFactory builds the provider files.open registers for a resolved
// session. rootPath is the optional D2 override — the verified OSC 7 cwd —
// and is empty when the caller omitted it. Same shape as the transport's
// FilesystemProviderFactory; the composition root adapts (AD-8, the
// dependency stays filesystem ← capability).
type ProviderFactory func(sess session.Session, rootPath string) (filesystem.Provider, error)

// filesystemEndpointAttester is the optional attestation surface a provider
// may carry (spec §5.1): the composition root wraps the sftp provider with
// the v1 endpoint id so the binding's endpointId is backend-attested rather
// than client-supplied. A local provider never implements it — an empty
// attestation is what makes files.reveal a local-only capability (D4).
type filesystemEndpointAttester interface {
	EndpointID() string
}

// FilesystemOpenService is the files.open surface: resolve the session,
// register the provider, and hold the binding surface for the inline root
// acquisition. It is what a FilesystemOpenOperation hands its callback.
type FilesystemOpenService interface {
	// Get resolves the session the connection owns (D15).
	Get(id session.ID) (session.Session, error)
	// OpenBinding builds the provider for the session through the wired
	// factory and registers it, returning the minted binding id and the
	// endpoint attestation (empty for local providers — what makes
	// files.reveal local-only, D4).
	OpenBinding(ctx context.Context, sess session.Session, rootPath string) (bindingID, endpointID string, err error)
	// Acquire takes the use-guard for one binding — the same per-call
	// authorisation every files.* method applies (D15).
	Acquire(id string, caller filesystem.Caller) (filesystem.Handle, func(), error)
}

// FilesystemOpenOperation is the typed operation for files.open. Its gates
// are [session, filesystem]: opening resolves the session and registers
// the provider.
type FilesystemOpenOperation interface {
	Run(context.Context, func(context.Context, FilesystemOpenService) error) error
}

// NewFilesystemOpenOperation builds the files.open operation, acquiring
// sessionGate before filesystemGate (the canonical order), then the
// execution lane.
func NewFilesystemOpenOperation(
	sessionGate, filesystemGate, lane control.Admission,
	registry session.Registry,
	factory ProviderFactory,
	reg *filesystem.Registry,
) FilesystemOpenOperation {
	g := &guard{}
	return newOperation[FilesystemOpenService](
		control.NewComposite(sessionGate, filesystemGate, lane),
		g,
		newFilesystemOpenService(g, registry, factory, reg),
	)
}

// newFilesystemOpenService builds the concrete files.open service bound to
// guard g.
func newFilesystemOpenService(g *guard, registry session.Registry, factory ProviderFactory, reg *filesystem.Registry) *filesystemOpenService {
	return &filesystemOpenService{guard: g, registry: registry, factory: factory, reg: reg}
}

type filesystemOpenService struct {
	guard    *guard
	registry session.Registry
	factory  ProviderFactory
	reg      *filesystem.Registry
}

func (s *filesystemOpenService) Get(id session.ID) (session.Session, error) {
	if err := s.guard.check(); err != nil {
		return nil, err
	}
	return s.registry.Get(id)
}

func (s *filesystemOpenService) OpenBinding(ctx context.Context, sess session.Session, rootPath string) (string, string, error) {
	if err := s.guard.check(); err != nil {
		return "", "", err
	}
	provider, err := s.factory(sess, rootPath)
	if err != nil {
		return "", "", err
	}
	endpointID := ""
	if a, ok := provider.(filesystemEndpointAttester); ok {
		endpointID = a.EndpointID()
	}
	// The write seam is resolved HERE, beside the attestation, because this
	// is the last moment the provider is in hand: Binding.provider is
	// unexported and Acquire returns a Handle, so nothing downstream can
	// perform this assertion (upload design D7). A provider that does not
	// implement it contributes no sink, and that nil is rule R1: the
	// binding's Upload refuses because it has nothing to write through, not
	// because somebody remembered to check where the tab is.
	//
	// Both shipped providers implement it, so the nil branch is the seam's
	// shape rather than a case in production. It stayed an assertion rather
	// than becoming a required Provider method because Provider is read-only
	// by contract (§5.1) and because R1 must keep being expressible: the
	// next provider that cannot write inherits the refusal here without
	// anybody adding a check for it.
	// The read-stream seam is resolved in the same breath and for the same
	// reasons, one direction over: reading a file off the wrong host is as
	// wrong as writing to it, so R1 is a nil Source here exactly as it is a
	// nil Sink above. Both assertions are here rather than two lines apart
	// in two files because they answer one question — what can this
	// provider do — and a second site would be a second place to forget.
	var caps filesystem.Capabilities
	if u, ok := provider.(filesystem.Uploader); ok {
		caps.Sink = u.Sink()
	}
	if d, ok := provider.(filesystem.Downloader); ok {
		caps.Source = d.Source()
	}
	bid, err := s.reg.Register(provider, sess.ID(), endpointID, caps)
	if err != nil {
		return "", "", err
	}
	return bid, endpointID, nil
}

func (s *filesystemOpenService) Acquire(id string, caller filesystem.Caller) (filesystem.Handle, func(), error) {
	if err := s.guard.check(); err != nil {
		return nil, nil, err
	}
	return s.reg.Acquire(id, caller)
}

// FilesystemBindingService is the per-binding filesystem surface: every
// files.* method after files.open. A binding id is validated per call by
// Acquire — bindings close at any moment, so a construction-time check
// would be a lie.
type FilesystemBindingService interface {
	Acquire(id string, caller filesystem.Caller) (filesystem.Handle, func(), error)
	Close(id string) error
	CloseSession(sessionID session.ID)
}

// FilesystemBindingOperation is the typed operation for the
// filesystem-binding domain. Its gate is [filesystem].
type FilesystemBindingOperation interface {
	Run(context.Context, func(context.Context, FilesystemBindingService) error) error
}

// NewFilesystemBindingOperation builds the filesystem-binding operation,
// acquiring the filesystem gate before the execution lane.
func NewFilesystemBindingOperation(filesystemGate, lane control.Admission, reg *filesystem.Registry) FilesystemBindingOperation {
	g := &guard{}
	return newOperation[FilesystemBindingService](control.NewComposite(filesystemGate, lane), g, newFilesystemBindingService(g, reg))
}

// newFilesystemBindingService builds the concrete filesystem-binding service
// bound to guard g.
func newFilesystemBindingService(g *guard, reg *filesystem.Registry) *filesystemBindingService {
	return &filesystemBindingService{guard: g, reg: reg}
}

type filesystemBindingService struct {
	guard *guard
	reg   *filesystem.Registry
}

func (s *filesystemBindingService) Acquire(id string, caller filesystem.Caller) (filesystem.Handle, func(), error) {
	if err := s.guard.check(); err != nil {
		return nil, nil, err
	}
	return s.reg.Acquire(id, caller)
}

func (s *filesystemBindingService) Close(id string) error {
	if err := s.guard.check(); err != nil {
		return err
	}
	return s.reg.Close(id)
}

func (s *filesystemBindingService) CloseSession(sessionID session.ID) {
	if !s.guard.ok() {
		return
	}
	s.reg.CloseSession(sessionID)
}
