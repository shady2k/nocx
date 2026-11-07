package transport

// A mutation's identity on the wire, and the one method that answers for it
// (ADR-8eej3e66 §4).
//
// THE IDENTITY TRAVELS BESIDE THE CALL, not inside its params. It is a fact
// about the invocation — like the origin the catalogue puts on an Invocation —
// and putting it in every mutation's params would mean 120 schemas each saying
// the same thing, a new one per method that arrives. So the request frame
// carries `mutation` beside `id` and `method`, the dispatch path reads it, and
// no handler ever sees it.
//
// THE ANSWER IS FOR THE PERSON WHO STOPPED WAITING. A mutation that was
// cancelled, expired, lost its socket, or was replayed by a client that was
// not sure gets one of three answers (ADR-8eej3e66 §8): it did not start, it
// is done, or it is still unknown and the question can be asked again while
// the record is retained. A timeout never implies a rollback, and nothing is
// replayed automatically: the one replay the record sanctions is the vault's
// unlock retry, and only because a sealed refusal means nothing was touched.
//
// NOTHING HERE LOGS PER CALL. A mutation's record is a fact a caller can ask
// for; a line per ordinary write would be the noise this catalogue exists to
// stop.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/operation/catalog"
	"github.com/shady2k/nocx/internal/transport/control"
)

// MutationIdentityLimit bounds the identity the renderer mints. A UUID is 36
// bytes; the bound is generous for any other client and small enough that the
// record map cannot be inflated per call.
const MutationIdentityLimit = 128

// MutationReplayErrorCode is the JSON-RPC error code for a mutation whose
// identity the backend has already seen with an outcome that forbids a second
// run, in the server-error range without colliding with the vault's
// -32000..-32005.
const MutationReplayErrorCode = -32005

// MutationReplayMessage is the fixed message of the refusal. The machine-
// readable payload carries the answer; this is what a log or a test reads.
const MutationReplayMessage = "This mutation already ran"

// mutationReplayReason is the fixed reason vocabulary in the error's data, the
// discriminator the renderer matches on.
const mutationReplayReason = "mutation-replayed"

type mutationReplayData struct {
	Reason    string `json:"reason"`
	Outcome   string `json:"outcome"`
	Operation string `json:"operation"`
}

// operationOutcomeParams is operation.outcome's params: the identity only.
// Contract: contracts/operation.outcome.params.schema.json.
type operationOutcomeParams struct {
	Mutation string `json:"mutation"`
}

// operationOutcomeResult is operation.outcome's result. `outcome` is the
// closed set of answers; `operation` names the catalogue operation the record
// belongs to, empty where no record exists, so a surface can say what to look
// at instead of "unknown" alone. Contract: contracts/operation.outcome.schema.json.
type operationOutcomeResult struct {
	Outcome   string `json:"outcome"`
	Operation string `json:"operation"`
}

// validateMutationIdentity is the envelope's own bound. An identity is opaque
// text the backend never parses; it may not be empty, may not carry control
// characters, and may not exceed the limit.
func validateMutationIdentity(id string) string {
	if id == "" {
		return ""
	}
	if !utf8.ValidString(id) || len(id) > MutationIdentityLimit {
		return fmt.Sprintf("mutation must be valid UTF-8 and at most %d bytes", MutationIdentityLimit)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return "mutation must not contain control characters"
		}
	}
	return ""
}

// inFlightMutations maps a request id to the mutation record its response
// answers. It is per connection and short-lived: an entry exists from dispatch
// until the handler writes its one response (or returns without writing one).
type inFlightMutations struct {
	mu        sync.Mutex
	byRequest map[string]*catalog.MutationRecord
}

func (m *inFlightMutations) add(id json.RawMessage, record *catalog.MutationRecord) {
	if len(id) == 0 || record == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byRequest == nil {
		m.byRequest = make(map[string]*catalog.MutationRecord)
	}
	m.byRequest[string(id)] = record
}

// take removes and returns the record this response answers.
func (m *inFlightMutations) take(id json.RawMessage) *catalog.MutationRecord {
	if len(id) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.byRequest[string(id)]
	delete(m.byRequest, string(id))
	return record
}

// mutationReplayError is the refusal of a mutation whose identity an earlier
// invocation already used. Every field is server vocabulary: the operation and
// the answer come from the retained record, never from the request.
func mutationReplayError(operation catalog.ID, answer catalog.MutationAnswer) RPCError {
	return RPCError{
		Code:    MutationReplayErrorCode,
		Message: MutationReplayMessage,
		Data: mutationReplayData{
			Reason:    mutationReplayReason,
			Outcome:   string(answer.Answer),
			Operation: string(operation),
		},
	}
}

// validateOperationOutcomeRaw checks operation.outcome's params: exactly one
// member, a non-empty identity within the bound.
func validateOperationOutcomeRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "params are required"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return "params must be a JSON object"
	}
	if len(fields) != 1 {
		return "params must contain only mutation"
	}
	value, ok := fields["mutation"]
	if !ok || len(value) == 0 {
		return "mutation is required"
	}
	var identity string
	if err := json.Unmarshal(value, &identity); err != nil {
		return "mutation must be a string"
	}
	if identity == "" {
		return "mutation must not be empty"
	}
	if len(identity) > MutationIdentityLimit {
		return fmt.Sprintf("mutation exceeds %d bytes", MutationIdentityLimit)
	}
	if refusal := validateMutationIdentity(identity); refusal != "" {
		return refusal
	}
	return ""
}

// operationOutcomeSpec registers operation.outcome on the ordinary work
// submission.
//
// NOT on the ingress-critical submission. The answer is a map lookup and holds
// nothing, but ADR-8eej3e66 §7 enumerates the traffic exempt from credit —
// cancel, ack, the resolvers, ping, diagnostics — and this method is not among
// them; the closed set in registration.go is not a place to route around a
// record. It is answered like any other call: it may wait for credit in the
// renderer's own queue, and it is never refused for capacity (stage 8), so
// "what became of my mutation" cannot be turned into the busyness answer it
// exists to end.
func (s *WSServer) operationOutcomeSpec(sub control.Submission) methodSpec {
	return reg(sub, catalog.OpOperationOutcome, params(validateOperationOutcomeRaw),
		func(_ *wsConn, _ *connState, r Responder) handlerFunc {
			return func(_ context.Context, req jsonrpcRequest) {
				var p operationOutcomeParams
				if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.Mutation) == "" {
					_ = r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: mutation is required"})
					return
				}
				answer := s.mutations.Answer(p.Mutation)
				_ = r.TryResult(req.ID, mustMarshal(operationOutcomeResult{
					Outcome:   string(answer.Answer),
					Operation: string(answer.Operation),
				}))
			}
		})
}

// beginMutation records a mutation identity for the invocation, or refuses a
// replay. The declaration decides whether an identity is meaningful: an
// operation whose expiry is outcome-unknown is one that may have an effect the
// caller cannot see.
func (s *WSServer) beginMutation(req jsonrpcRequest, op catalog.Method) (*catalog.MutationRecord, *RPCError) {
	if req.Mutation == "" {
		return nil, nil
	}
	if refusal := validateMutationIdentity(req.Mutation); refusal != "" {
		return nil, &RPCError{Code: -32602, Message: "Invalid params: " + refusal}
	}
	if op.Declaration().Expired != catalog.ExpiredOutcomeUnknown {
		// A read keeps no record: an identity on one is accepted and ignored,
		// because the caller may mint one without knowing the declaration.
		return nil, nil
	}
	record, answer := s.mutations.Begin(req.Mutation, op.ID())
	if record == nil {
		err := mutationReplayError(op.ID(), answer)
		return nil, &err
	}
	return record, nil
}
