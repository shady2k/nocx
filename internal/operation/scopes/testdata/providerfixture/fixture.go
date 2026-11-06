// Package providerfixture is the planted shape the type-based scope check must
// catch. It is under testdata, so the go tool never builds it as part of the
// module.
package providerfixture

type Provider interface {
	Close(id string) error
	List() int
}

type Server struct {
	filesys Provider
}

func direct(s *Server) {
	_ = s.filesys.Close("binding")
}

func alias(s *Server) {
	p := s.filesys
	_ = p.Close("binding")
}

func otherReceiver(server *Server) {
	_ = server.filesys.Close("binding")
}

type Lookalike struct{}

func (Lookalike) Close(string) error { return nil }

// notProvider spells the same method on an unrelated type: it is not a
// provider call and must not be reported.
func notProvider(l Lookalike) {
	_ = l.Close("binding")
}

type closer interface {
	Close(string) error
}

func methodValue(s *Server) {
	closeBinding := s.filesys.Close
	_ = closeBinding("binding")
}

func callback(s *Server, register func(func(string) error)) {
	register(s.filesys.Close)
}

func redeclaredInterface(s *Server) {
	var c closer = s.filesys
	_ = c.Close("binding")
}
