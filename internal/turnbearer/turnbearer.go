// Package turnbearer mints and retires the bearer that admits one assistant
// turn's agent to nocx's tool endpoint.
//
// # Why a bearer at all
//
// An agent in a pane reaches the tool endpoint as a process inside that pane's
// tree, and the endpoint decides which pane from the kernel's peer credentials.
// The assistant's agent is not in a pane's tree: nocx-server starts it, so the
// process tree says "the backend" and nothing more. ADR-anntnxdj decision 3
// gives that case its own way in: nocx-server mints a bearer for the turn,
// hands it to the MCP bridge the agent launches (nocx-helper mcp --socket) in
// the child's environment, and retires it when the turn ends. The endpoint
// checks the bearer at admission and selects the assistant's chain.
//
// # What the bearer carries
//
// The assistant's already-built run handle. The handle owns the complete bound
// context, grant minting at admission and refresh after an answer, and the same
// authority pipeline the in-process caller uses.
//
// # The secret
//
// The bearer is 32 random bytes in lower-case hex. It is compared in constant
// time at admission (internal/app's authorizer, through Registry.Lookup), it
// never appears in the bridge argv, and nothing here logs it.
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
	"github.com/shady2k/nocx/internal/toolendpoint/panebind"
)

// Authority binds a bearer to the assistant's already-built run, not a
// reconstruction of its grant or pane capabilities.
type Authority struct {
	Tools    assistant.TurnTools
	lifetime *turnLifetime
}

// turnLifetime serializes publication with retirement without retaining a
// permanent endpoint tombstone for every completed turn.
type turnLifetime struct {
	mu   sync.Mutex
	live bool
}

// Publish makes publication part of the live bearer decision. A lookup made
// before retirement cannot publish after retirement has closed this lifetime.
func (a Authority) Publish(publish func() bool) bool {
	if a.lifetime == nil {
		return false
	}
	a.lifetime.mu.Lock()
	defer a.lifetime.mu.Unlock()
	return a.lifetime.live && publish()
}

// TurnBridge is the MCP server entry the agent launches for one turn: the argv
// and the environment. It is deliberately the same two fields internal/harness's
// TurnSpec carries, so the next stage maps one to the other without a
// translation table.
type TurnBridge struct {
	Command []string
	Env     []string
}

// Registry answers which live turn a presented bearer names. It is the
// authorizer's view of a Minter, so the authorizer cannot mint.
type Registry interface {
	Lookup(token string) (Authority, bool)
}

// Minter is the live set of assistant turns. It mints one bearer per turn,
// hands back the bridge that presents it, and retires it with the turn.
type Minter struct {
	endpoint Endpoint
	helper   string

	mu    sync.Mutex
	turns []turn
}

// Endpoint is the part of toolendpoint.Endpoint a turn needs: where the bridge
// connects, and how the turn's connections are closed when it retires.
type Endpoint interface {
	SocketPath() string
	RetireAdmissions(scope string, epoch toolendpoint.AdmissionEpoch) int
}

type turn struct {
	token     string
	authority Authority
}

// NewMinter builds the set of live turns over the endpoint their bridges reach
// and the helper binary those bridges exec.
func NewMinter(endpoint Endpoint, helper string) *Minter {
	return &Minter{endpoint: endpoint, helper: helper}
}

// Mint registers one turn, and returns the bridge that presents its bearer and
// the function that retires it.
//
// Retire refuses further admissions of this bearer and closes the connections
// admitted under the turn — which cancels the calls in flight on them. It is
// idempotent: a turn ends once.
func (m *Minter) Mint(authority Authority) (TurnBridge, func(), error) {
	switch {
	case m == nil || m.endpoint == nil:
		return TurnBridge{}, nil, errors.New("turnbearer: no endpoint")
	case m.helper == "":
		return TurnBridge{}, nil, errors.New("turnbearer: no helper binary for the bridge")
	case authority.Tools == nil:
		return TurnBridge{}, nil, errors.New("turnbearer: no assistant run")
	case authority.Tools.RunContext().RunID == "":
		return TurnBridge{}, nil, errors.New("turnbearer: no run id")
	case authority.Tools.RunContext().Session == "":
		return TurnBridge{}, nil, errors.New("turnbearer: no session")
	}
	token, err := mintToken()
	if err != nil {
		return TurnBridge{}, nil, err
	}
	m.mu.Lock()
	authority.lifetime = &turnLifetime{live: true}
	m.turns = append(m.turns, turn{token: token, authority: authority})
	m.mu.Unlock()

	bridge := TurnBridge{
		Command: []string{m.helper, "mcp", "--socket", m.endpoint.SocketPath()},
		Env:     []string{panebind.TokenEnvVar + "=" + token},
	}
	var once sync.Once
	retire := func() {
		once.Do(func() {
			// Close publication first. A lookup already in flight still holds
			// this lifetime and cannot publish after the live bit is cleared.
			// Then remove the secret and close any published connection.
			authority.lifetime.mu.Lock()
			authority.lifetime.live = false
			authority.lifetime.mu.Unlock()
			m.forget(token)
			m.endpoint.RetireAdmissions(toolendpoint.TurnScope(authority.Tools.RunContext().RunID), toolendpoint.TurnEpoch)
		})
	}
	return bridge, retire, nil
}

// Lookup answers which live turn a presented bearer names.
//
// THE COMPARISON IS CONSTANT TIME AND DOES NOT STOP AT A MATCH: the bearer is a
// secret that admits, and a comparison that returned at the first difference
// would tell a caller how much of a guess was right. The set is small — one
// turn per assistant run in flight — so comparing all of it is cheap.
func (m *Minter) Lookup(token string) (Authority, bool) {
	if m == nil || token == "" {
		return Authority{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var found Authority
	matched := 0
	for _, t := range m.turns {
		if subtle.ConstantTimeCompare([]byte(t.token), []byte(token)) == 1 {
			found = t.authority
			matched = 1
		}
	}
	return found, matched == 1
}

// forget removes one token. It is only reached from the retire closure, which
// holds the token this Minter minted, so the comparison does not have to hide
// anything from a caller — but it is the same comparison, so there is one rule
// for "is this the token" in the package.
func (m *Minter) forget(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.turns[:0]
	for _, t := range m.turns {
		if subtle.ConstantTimeCompare([]byte(t.token), []byte(token)) != 1 {
			kept = append(kept, t)
		}
	}
	clear(m.turns[len(kept):])
	m.turns = kept
}

// mintToken produces one bearer: 32 random bytes, spelled the one way
// panebind accepts.
func mintToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("turnbearer: mint: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
