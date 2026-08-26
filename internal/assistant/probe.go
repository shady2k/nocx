package assistant

// The probe — the Test button's client side, and agent.status's "last probe
// result" fact (design §4.5, bead notes). The Test button tests what will
// actually be used, not one cheap completion: this streams a real response
// through the same adk agent the ask transaction will use, over the same
// guarded HTTP client. A one-word answer keeps the probe cheap.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// probeTimeout bounds one probe: a Test button that can hang forever is a
// control that lies. The stream must produce its one-word answer well inside
// this; the connection's own cancellation still applies.
const probeTimeout = 30 * time.Second

// probePrompt asks for the smallest honest completion: one word. The probe
// asserts streaming works end to end, not that the model is eloquent.
const probePrompt = "Reply with the single word: ok"

// Probe implements Client. The parameters are the form's draft values; a
// failed dial, a refused stream, a timeout or zero content is a ProbeResult
// with OK=false — a probe outcome, never a Go error. A Go error means the
// probe could not run at all (an empty base URL), and the caller should not
// present it as an endpoint verdict.
//
// An empty Model routes to the CONNECTION check (connection.go), because
// "can I reach this API with this key" needs no model and is the only
// question askable of an endpoint nobody has typed a model into yet. The
// result's Kind names which check ran.
func (c *client) Probe(ctx context.Context, p ProbeParams) (ProbeResult, error) {
	if strings.TrimSpace(p.BaseURL) == "" {
		return ProbeResult{}, errProbeInvalid("base URL is required")
	}
	if strings.TrimSpace(p.Model) == "" {
		// No model is not a missing parameter — it is the other question
		// (nocx-q27y): "can I reach this API with this key", which needs
		// none. See connection.go.
		return c.probeConnection(ctx, p)
	}

	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var sb strings.Builder
	// The probe is the connectivity question, never a run with authority: it
	// always declares zero tools, which keeps it on the no-tools path — and
	// therefore has nothing that can suspend and no run id to key a
	// checkpoint by. It passes the client's store with an empty id: nothing
	// is written, nothing is looked up.
	streamErr := streamModelAnswer(probeCtx, c.log, c.http, p.Key, p.BaseURL, p.Model, p.Headers,
		[]*schema.Message{schema.UserMessage(probePrompt)}, nil, nil, c.checkpoints, "",
		// The probe asks "does this model answer", so only the ANSWER
		// counts: a reasoning model that thought and said nothing has not
		// answered, and a probe that accepted its thinking would report a
		// working endpoint on a stream with no reply in it.
		func(e AskEvent) error {
			if e.Kind == AskAnswer {
				sb.WriteString(e.Text)
			}
			return nil
		})

	res := ProbeResult{
		EndpointName: p.Name,
		Model:        p.Model,
		Kind:         ProbeModel,
		ElapsedMS:    time.Since(start).Milliseconds(),
		At:           time.Now(),
	}
	switch {
	case streamErr != nil:
		res.OK = false
		if sentence, ok := EndpointErrorSentence(streamErr, p.Model); ok {
			res.Error = sentence
		} else {
			var streamFailure *StreamError
			if errors.As(streamErr, &streamFailure) {
				res.Error = streamFailure.Message
			} else {
				res.Error = UnexplainedFailureSentence
			}
		}
	case sb.Len() == 0:
		// A completed stream that produced no text: the model replied with
		// nothing (or with a hallucinated tool call, which in explain mode
		// has no consumer). The endpoint answered; it did not answer.
		res.OK = false
		res.Error = "the model returned no text"
	default:
		res.OK = true
	}
	return res, nil
}

// errProbeInvalid marks a probe that could not run.
type errProbeInvalid string

func (e errProbeInvalid) Error() string { return "probe: " + string(e) }

// ProbeStore keeps the last probe result — agent.status's "last probe
// result" fact (design §7). Process-lifetime, like the transport's SSH
// probe store: a probe is diagnostic evidence whose meaning expires with
// the endpoint that produced it, and preserving it across restarts would
// report a result whose inputs cannot be reconstructed.
type ProbeStore struct {
	mu   sync.Mutex
	last *ProbeResult
}

// NewProbeStore creates an empty store.
func NewProbeStore() *ProbeStore { return &ProbeStore{} }

// Record stores r as the last probe result.
func (s *ProbeStore) Record(r ProbeResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := r
	s.last = &cp
}

// Last returns a copy of the last recorded result, or nil when none.
func (s *ProbeStore) Last() *ProbeResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return nil
	}
	cp := *s.last
	return &cp
}
