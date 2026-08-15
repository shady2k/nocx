package assistant

// The connection check — the other half of the Test button (nocx-q27y).
//
// "Can I reach this API with this key" needs no model, and it is the only
// question that can be asked of an endpoint nobody has typed a model into
// yet. GET {baseURL}/models answers it in one request: the address resolves,
// TLS completes, the credential is accepted or refused, and the response
// names what the endpoint offers.
//
// TWO DISTINCTIONS THIS FILE EXISTS TO KEEP, both of which a later reader
// will be tempted to collapse:
//
//  1. Reaching a server and being told "no such route" is NOT a failure to
//     reach it. GET /models is not universally implemented — plenty of
//     OpenAI-compatible servers 404 it and stream completions perfectly. So
//     a 404 or 405 is a SUCCESSFUL connection check that found no models,
//     never a broken endpoint.
//  2. The discovered list is an ADDITION, never a gate. An endpoint that
//     lists nothing must stay fully configurable by hand; a picker that
//     gated configuration would turn a dead button into a dead form, which
//     is worse than what it replaced.
//
// The request rides the same guarded client as the streaming probe, so the
// dial-time address rule, the per-redirect re-check and the drop-the-
// credential-on-origin-change rule all apply unchanged (httpguard.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// connectTimeout bounds one connection check. Shorter than probeTimeout:
// this is one small GET, not a streamed completion, and a Test button that
// hangs is the control this whole bead is about.
const connectTimeout = 15 * time.Second

// maxModelListBytes caps what we read from /models. A cooperative endpoint
// answers in kilobytes; an uncooperative one must not be able to hold the
// button open by streaming forever.
const maxModelListBytes = 1 << 20

// modelListResponse is the OpenAI /models shape, read leniently: we want the
// ids and nothing else, and an endpoint that answers 200 with a shape we do
// not recognise is still REACHABLE — the point of the check.
type modelListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// probeConnection performs the no-model check. Like Probe, a dial failure or
// a refused credential is a RESULT with OK=false, never a Go error; a Go
// error means the check could not run at all.
func (c *client) probeConnection(ctx context.Context, p ProbeParams) (ProbeResult, error) {
	base := strings.TrimSpace(p.BaseURL)
	if base == "" {
		return ProbeResult{}, errProbeInvalid("base URL is required")
	}

	start := time.Now()
	res := ProbeResult{
		EndpointName: p.Name,
		Kind:         ProbeConnection,
	}
	finish := func() ProbeResult {
		res.ElapsedMS = time.Since(start).Milliseconds()
		res.At = time.Now()
		return res
	}

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(connectCtx, http.MethodGet,
		strings.TrimSuffix(base, "/")+"/models", nil)
	if err != nil {
		res.Error = err.Error()
		return finish(), nil
	}
	// The credential goes on only when there is one: a local model server
	// that needs none must not be sent an empty Bearer, which some reject.
	// Use is the single binding accessor — the key is written straight into
	// the header inside it and never copied to a variable of ours.
	if !p.Key.IsEmpty() {
		_ = p.Key.Use(func(key []byte) error {
			req.Header.Set("Authorization", "Bearer "+string(key))
			return nil
		})
	}
	// The endpoint's custom headers ride the connection check too, so a Test
	// that passes means the real calls will: a gateway that wants a tenant
	// header refuses both the probe and the completion equally. Their
	// canonical names tag the context so the redirect rule drops exactly
	// them on an origin change (httpguard.go) — the same treatment the
	// credential gets.
	if m, names := headerMap(p.Headers); m != nil {
		for name, value := range m {
			req.Header.Set(name, value)
		}
		req = req.WithContext(withCustomHeaderNames(req.Context(), names))
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Never reached the endpoint: the dial, the address rule, TLS, the
		// timeout. This is the failure the button exists to report.
		res.Error = err.Error()
		return finish(), nil
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		res.Error = fmt.Sprintf("the endpoint refused the credential (HTTP %d)", resp.StatusCode)
		return finish(), nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		// Reached it; it does not implement /models. A successful
		// connection check that found nothing to list — see the header.
		res.OK = true
		return finish(), nil
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		res.Error = fmt.Sprintf("the endpoint answered HTTP %d", resp.StatusCode)
		return finish(), nil
	}

	res.OK = true
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if err != nil {
		// Reached and accepted; we simply could not read the list. Still a
		// good connection — the models are the addition, not the verdict.
		return finish(), nil
	}
	var parsed modelListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return finish(), nil
	}
	for _, m := range parsed.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			res.Models = append(res.Models, id)
		}
	}
	return finish(), nil
}
