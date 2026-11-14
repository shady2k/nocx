// Package turnbearer owns the session bearer that admits an assistant's agent
// to nocx's tool endpoint. A bearer lives for one agent process; each connection
// is bound to the immutable authority of the turn running at admission.
//
// The session actor mints once, calls BeginTurn before sending a prompt, and
// waits for EndTurn before publishing a terminal or admitting the next turn.
// Pending includes accepted calls waiting for approval; an idle open connection
// does not count. A cancelled turn ends its session: the actor must retire the
// bearer rather than start another turn on it (ADR-4khgzeyp decisions 3 and 5).
package turnbearer

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/toolendpoint/turnbind"
)

// Authority binds an admission to the assistant's already-built run. It is
// never rebound when the session starts another turn.
type Authority struct {
	Tools    assistant.TurnTools
	lifetime *turnLifetime
}

type turnLifetime struct {
	mu   sync.Mutex
	live bool
}

// Publish serializes admission publication with the end of this authority.
// An earlier lookup cannot publish a connection after EndTurn closes it.
func (a Authority) Publish(publish func() bool) bool {
	if a.lifetime == nil {
		return false
	}
	a.lifetime.mu.Lock()
	defer a.lifetime.mu.Unlock()
	return a.lifetime.live && publish()
}

// TurnBridge is the MCP bridge command and private environment of a session.
// The bearer is 32 random bytes in lower-case hex and never appears in argv.
type TurnBridge struct {
	Command []string
	Env     []string
}

// Registry is the authorizer's read-only view of the session bearers.
type Registry interface {
	Lookup(token string) (Authority, bool)
}

// Endpoint supplies turn-scoped call settlement and activity information.
// The owner closes publication before settling admissions.
type Endpoint interface {
	SocketPath() string
	SettleAdmissions(scope string, epoch toolendpoint.AdmissionEpoch)
	PendingAdmissions(scope string, epoch toolendpoint.AdmissionEpoch) bool
}

// Minter owns session bearers and each session's current turn authority.
type Minter struct {
	endpoint Endpoint
	helper   string
	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	token     string
	authority Authority
	settling  chan struct{}
	retired   bool
}

// NewMinter constructs the session registry without touching the endpoint.
func NewMinter(endpoint Endpoint, helper string) *Minter {
	return &Minter{endpoint: endpoint, helper: helper, sessions: make(map[string]*session)}
}

// Mint registers a session without admitting tools until BeginTurn. Retire
// removes its bearer, closes publication, and waits for accepted calls to
// settle. The returned retirement function is idempotent.
func (m *Minter) Mint(id string) (TurnBridge, func(), error) {
	switch {
	case m == nil || m.endpoint == nil:
		return TurnBridge{}, nil, errors.New("turnbearer: no endpoint")
	case m.helper == "":
		return TurnBridge{}, nil, errors.New("turnbearer: no helper binary for the bridge")
	case id == "":
		return TurnBridge{}, nil, errors.New("turnbearer: no session")
	}
	token, err := mintToken()
	if err != nil {
		return TurnBridge{}, nil, err
	}
	s := &session{token: token}
	m.mu.Lock()
	if _, exists := m.sessions[id]; exists {
		m.mu.Unlock()
		return TurnBridge{}, nil, errors.New("turnbearer: session already minted")
	}
	m.sessions[id] = s
	m.mu.Unlock()
	bridge := TurnBridge{
		Command: []string{m.helper, "mcp", "--socket", m.endpoint.SocketPath()},
		Env:     []string{turnbind.TokenEnvVar + "=" + token},
	}
	var once sync.Once
	retire := func() {
		once.Do(func() {
			m.mu.Lock()
			s.retired = true
			delete(m.sessions, id)
			s.token = ""
			m.mu.Unlock()
			m.endTurn(s)
		})
	}
	return bridge, retire, nil
}

// BeginTurn attaches fresh authority to a minted session. The previous turn
// must have finished settlement before another turn can begin.
func (m *Minter) BeginTurn(id string, authority Authority) error {
	switch {
	case authority.Tools == nil:
		return errors.New("turnbearer: no assistant run")
	case authority.Tools.RunContext().RunID == "":
		return errors.New("turnbearer: no run id")
	case authority.Tools.RunContext().Session == "":
		return errors.New("turnbearer: no run session")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil || s.retired {
		return errors.New("turnbearer: unknown session")
	}
	if s.authority.lifetime != nil {
		return errors.New("turnbearer: session already has a turn")
	}
	authority.lifetime = &turnLifetime{live: true}
	s.authority = authority
	return nil
}

// EndTurn refuses new admissions, closes turn connections and waits until all
// accepted calls have settled. The session bearer remains available for the
// next BeginTurn. Ending an idle or unknown session is a no-op.
func (m *Minter) EndTurn(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s != nil {
		m.endTurn(s)
	}
}

func (m *Minter) endTurn(s *session) {
	m.mu.Lock()
	if s.settling != nil {
		done := s.settling
		m.mu.Unlock()
		<-done
		return
	}
	authority := s.authority
	if authority.lifetime == nil {
		m.mu.Unlock()
		return
	}
	done := make(chan struct{})
	s.settling = done
	m.mu.Unlock()
	authority.lifetime.mu.Lock()
	authority.lifetime.live = false
	authority.lifetime.mu.Unlock()
	m.endpoint.SettleAdmissions(toolendpoint.TurnScope(authority.Tools.RunContext().RunID), toolendpoint.TurnEpoch)
	m.mu.Lock()
	s.authority = Authority{}
	s.settling = nil
	close(done)
	m.mu.Unlock()
}

// Pending reports accepted tool work, including an approval waiting for an
// answer. Merely having an open bridge connection is not pending activity.
func (m *Minter) Pending(id string) bool {
	m.mu.Lock()
	s := m.sessions[id]
	var authority Authority
	if s != nil {
		authority = s.authority
	}
	m.mu.Unlock()
	if authority.Tools == nil {
		return false
	}
	return m.endpoint.PendingAdmissions(toolendpoint.TurnScope(authority.Tools.RunContext().RunID), toolendpoint.TurnEpoch)
}

// Lookup returns the turn authority running now, never authority from an idle
// session. Every bearer is compared in constant time without stopping at a
// match. Publication must still pass Authority.Publish.
func (m *Minter) Lookup(token string) (Authority, bool) {
	if m == nil || token == "" {
		return Authority{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var found Authority
	matched := false
	for _, s := range m.sessions {
		if subtle.ConstantTimeCompare([]byte(s.token), []byte(token)) == 1 && s.authority.lifetime != nil && s.settling == nil {
			found = s.authority
			matched = true
		}
	}
	return found, matched
}

func mintToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("turnbearer: mint: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
