package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/shady2k/nocx/internal/agentapproval"
	"github.com/shady2k/nocx/internal/agentrecord"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/shellintegration"
)

const maxAgentLaunchTickets = 1024

var errAgentLaunchTicketUnavailable = errors.New("local agent launch ticket is unavailable")

type agentLaunchSnapshot struct {
	executable agentapproval.Executable
	argv       []string
	env        []string
	payload    string
}

type agentLaunchKey struct {
	transport lifecycle.TransportID
	lane      lifecycle.LaneID
	domain    lifecycle.DomainID
	epoch     uint64
}

func launchKey(binding lifecyclepub.AgentLaunchBinding) agentLaunchKey {
	return agentLaunchKey{transport: binding.Transport, lane: binding.Lane, domain: binding.Domain, epoch: binding.Epoch}
}

type agentLaunchTicket struct {
	binding  lifecyclepub.AgentLaunchBinding
	snapshot agentLaunchSnapshot
	inFlight bool
}

// agentLaunchTickets is memory-only. A pending human question has no deadline,
// so tickets live until a final answer, explicit cancellation, binding cleanup,
// or app shutdown. The bound is refusal-only: a live ticket is never evicted.
type agentLaunchTickets struct {
	mu      sync.Mutex
	random  io.Reader
	byToken map[string]*agentLaunchTicket
	byKey   map[agentLaunchKey]string
}

func newAgentLaunchTickets() *agentLaunchTickets {
	return &agentLaunchTickets{random: rand.Reader, byToken: make(map[string]*agentLaunchTicket), byKey: make(map[agentLaunchKey]string)}
}

func (s *agentLaunchTickets) Issue(binding lifecyclepub.AgentLaunchBinding, snapshot agentLaunchSnapshot) (string, error) {
	if s == nil || binding.Agent == "" {
		return "", errAgentLaunchTicketUnavailable
	}
	var nonce [32]byte
	if _, err := io.ReadFull(s.random, nonce[:]); err != nil {
		return "", fmt.Errorf("agent launch ticket could not be created")
	}
	token := base64.RawURLEncoding.EncodeToString(nonce[:])
	key := launchKey(binding)
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.byKey[key]
	if !exists && len(s.byToken) >= maxAgentLaunchTickets {
		return "", errors.New("local agent launch ticket capacity is full")
	}
	if _, collision := s.byToken[token]; collision {
		return "", errors.New("local agent launch ticket collision")
	}
	if exists {
		delete(s.byToken, old)
	}
	s.byToken[token] = &agentLaunchTicket{binding: binding, snapshot: cloneAgentLaunchSnapshot(snapshot)}
	s.byKey[key] = token
	return token, nil
}

// Begin atomically reserves the ticket for one enrolment attempt and returns a
// copy of the resolved record snapshot. Pending releases the reservation so a
// retry can use the same ticket; Complete consumes it on every final outcome.
func (s *agentLaunchTickets) Begin(binding lifecyclepub.AgentLaunchBinding, token string) (agentLaunchSnapshot, error) {
	if s == nil {
		return agentLaunchSnapshot{}, errAgentLaunchTicketUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byToken[token]
	if !ok || entry.binding != binding || entry.inFlight || s.byKey[launchKey(binding)] != token {
		return agentLaunchSnapshot{}, errAgentLaunchTicketUnavailable
	}
	entry.inFlight = true
	return cloneAgentLaunchSnapshot(entry.snapshot), nil
}

func (s *agentLaunchTickets) RetainPending(binding lifecyclepub.AgentLaunchBinding, token string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byToken[token]
	if !ok || entry.binding != binding || !entry.inFlight {
		return false
	}
	entry.inFlight = false
	return true
}

func (s *agentLaunchTickets) Complete(binding lifecyclepub.AgentLaunchBinding, token string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byToken[token]
	if !ok || entry.binding != binding || !entry.inFlight {
		return false
	}
	delete(s.byToken, token)
	delete(s.byKey, launchKey(binding))
	return true
}

// Cancel requires the entire authenticated launch binding and exact ticket.
// A shell may cancel only its own agent record on its own lane and epoch.
func (s *agentLaunchTickets) Cancel(binding lifecyclepub.AgentLaunchBinding, token string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byToken[token]
	if !ok || entry.binding != binding || s.byKey[launchKey(binding)] != token {
		return false
	}
	s.removeLocked(token, entry)
	return true
}

func (s *agentLaunchTickets) InvalidateBinding(binding lifecyclepub.AgentLaunchBinding) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if token, ok := s.byKey[launchKey(binding)]; ok {
		if entry, exists := s.byToken[token]; exists {
			s.removeLocked(token, entry)
		}
	}
}

func (s *agentLaunchTickets) InvalidateDomain(transport lifecycle.TransportID, lane lifecycle.LaneID, domain lifecycle.DomainID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, entry := range s.byToken {
		if entry.binding.Transport == transport && entry.binding.Lane == lane && entry.binding.Domain == domain {
			s.removeLocked(token, entry)
		}
	}
}

func (s *agentLaunchTickets) InvalidateTransport(transport lifecycle.TransportID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, entry := range s.byToken {
		if entry.binding.Transport == transport {
			s.removeLocked(token, entry)
		}
	}
}

func (s *agentLaunchTickets) removeLocked(token string, entry *agentLaunchTicket) {
	delete(s.byToken, token)
	if s.byKey[launchKey(entry.binding)] == token {
		delete(s.byKey, launchKey(entry.binding))
	}
}

func cloneAgentLaunchSnapshot(snapshot agentLaunchSnapshot) agentLaunchSnapshot {
	snapshot.argv = append([]string(nil), snapshot.argv...)
	snapshot.env = append([]string(nil), snapshot.env...)
	return snapshot
}

// agentLaunchService is the only local resolver. It trusts locality only from
// the registered transport and reads the same record store Settings writes.
type agentLaunchService struct {
	records    *agentrecord.Store
	transports *transportRegistry
	tickets    *agentLaunchTickets
	workerLane func(lifecycle.LaneID) bool
}

func newAgentLaunchService(records *agentrecord.Store, transports *transportRegistry, tickets *agentLaunchTickets) *agentLaunchService {
	return &agentLaunchService{records: records, transports: transports, tickets: tickets}
}

func (s *agentLaunchService) Resolve(ctx context.Context, binding lifecyclepub.AgentLaunchBinding) (lifecyclepub.AgentLaunchResolution, error) {
	if s == nil || s.transports == nil {
		return lifecyclepub.AgentLaunchResolution{Local: true}, errors.New("local agent launch is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return lifecyclepub.AgentLaunchResolution{Local: true}, err
	}
	kind, ok := s.transports.lookup(binding.Transport)
	if !ok {
		return lifecyclepub.AgentLaunchResolution{Local: true}, errors.New("agent transport is not registered")
	}
	if !kind.local {
		return lifecyclepub.AgentLaunchResolution{}, nil
	}
	if s.workerLane != nil && s.workerLane(binding.Lane) {
		// workers.spawn owns a literal command line, not a Settings record.
		// Its pane still uses the enrolment wrapper, but resolution stays
		// conventional so configured record args/env cannot rewrite a worker.
		return lifecyclepub.AgentLaunchResolution{Local: false}, nil
	}
	refusal := func(reason string) (lifecyclepub.AgentLaunchResolution, error) {
		return lifecyclepub.AgentLaunchResolution{Local: true, Reason: reason}, nil
	}
	if s.records == nil || s.tickets == nil {
		return refusal("local agent records are unavailable")
	}
	entry, exists := s.records.Entry(binding.Agent)
	if !exists {
		return refusal("no local agent record exists")
	}
	if entry.State == agentrecord.StateUnreadable {
		return refusal("the local agent record cannot be read")
	}
	if entry.Record.Disabled {
		return refusal("this local agent is disabled")
	}
	executable, err := agentapproval.IdentityForExecutable(entry.Record.Command)
	if err != nil {
		return refusal("the local agent executable could not be resolved")
	}
	argv := make([]string, 1, 1+len(entry.Record.Args))
	argv[0] = executable.Path
	argv = append(argv, entry.Record.Args...)
	payload, err := shellintegration.EncodeAgentLaunchPayload(argv, entry.Record.Env)
	if err != nil {
		return refusal("the local agent record could not be encoded")
	}
	snapshot := agentLaunchSnapshot{executable: executable, argv: argv, env: append([]string(nil), entry.Record.Env...), payload: payload}
	ticket, err := s.tickets.Issue(binding, snapshot)
	if err != nil {
		return refusal(fmt.Sprint(err))
	}
	return lifecyclepub.AgentLaunchResolution{Local: true, Ticket: ticket, Payload: payload}, nil
}

func (s *agentLaunchService) Cancel(binding lifecyclepub.AgentLaunchBinding, ticket string) {
	s.tickets.Cancel(binding, ticket)
}

func (s *agentLaunchService) InvalidateBinding(binding lifecyclepub.AgentLaunchBinding) {
	s.tickets.InvalidateBinding(binding)
}

func (s *agentLaunchService) InvalidateDomain(t lifecycle.TransportID, lane lifecycle.LaneID, domain lifecycle.DomainID) {
	s.tickets.InvalidateDomain(t, lane, domain)
}

func (s *agentLaunchService) InvalidateTransport(t lifecycle.TransportID) {
	s.tickets.InvalidateTransport(t)
}
