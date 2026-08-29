// Package coordinator serves the nocx daemon's discovery socket: the
// newline-delimited JSON channel over which a launcher learns which daemon
// is running and how to reach its WebSocket.
//
// The split between this socket and the loopback TCP the WS server already
// listens on is forced, not stylistic (design §4). A WebView cannot speak a
// unix socket, so the renderer must have TCP; TCP has no peer credentials,
// which a unix socket does. So the socket is where trust is established and
// TCP is where the bytes flow, and the only thing that crosses from one to
// the other is the token the transport minted for this launch.
//
// The token is why the peer checks here are not defence in depth but the
// barrier itself: anything that reads this socket can attach to the
// backend. It therefore never reaches disk, argv, the environment or a log
// line — see [Server.serve], which is the only code that ever names it.
package coordinator

// ProtocolVersion is the version of THIS socket's request/response shapes.
// It is deliberately separate from the build version: two builds of the
// same release may speak it, and one build may not speak another's.
//
// A launcher compares its own constant with the daemon's and decides what
// to do about a mismatch. That comparison is not here — this package makes
// the fact available and states nothing about what a client should conclude
// from it.
const ProtocolVersion = 1

// RequestHello is the one request this socket answers today. Naming it
// rather than serving whatever arrives is what lets an unknown request be
// refused with a reason instead of silently ignored.
const RequestHello = "hello"

// Request is one line of the client-to-daemon direction.
type Request struct {
	Type string `json:"type"`
	// Client is who is asking. The daemon records nothing from it and
	// makes no decision on it in A1.0; it is here because the handshake
	// is symmetric by design (design §4) and a launcher that could not
	// state its own version could not be told it is mismatched.
	Client *ClientIdentity `json:"client,omitempty"`
}

// ClientIdentity is the launcher's own build and protocol version.
type ClientIdentity struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Protocol int    `json:"protocol"`
}

// Build identifies the binary the daemon was compiled from.
type Build struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// Hello is the four facts a client needs and cannot get any other way: who
// the daemon is (build and protocol), where its WebSocket is, and the
// capability that opens it.
type Hello struct {
	Build     Build  `json:"build"`
	Protocol  int    `json:"protocol"`
	WSAddress string `json:"wsAddress"`
	WSToken   string `json:"wsToken"`
}

// Response is one line of the daemon-to-client direction. Exactly one of
// the two fields is set: a refusal carries no payload, so a client that
// reads Hello without checking Error cannot mistake a refusal for an empty
// answer.
//
// This shape gets no schema in contracts/. That rule binds the JSON-RPC
// results the renderer decodes (AGENTS.md testing rule 5) and the renderer
// never sees this socket — it cannot open one. The wire test that matters
// here is the one that reads a real response off a real socket, which is
// what coordinator_test.go does.
type Response struct {
	Hello *Hello `json:"hello,omitempty"`
	Error string `json:"error,omitempty"`
}

// Backend is what the daemon's own transport looks like from here: an
// address to connect to and the token that opens it. Two methods rather
// than a *transport.WSServer, so this package neither imports the transport
// nor can reach anything else on it (AD-8).
type Backend interface {
	// WSAddress is the loopback TCP address the WS server is listening on.
	WSAddress() string
	// WSToken is the capability minted for this launch.
	WSToken() string
}
