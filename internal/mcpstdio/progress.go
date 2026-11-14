package mcpstdio

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"
)

// progressInterval is how often a long call reports progress to the client
// that asked for it, and the number is a product value rather than a taste.
//
// pi cuts an MCP request at 60 s by default. The measurement behind that
// number (ADR-anntnxdj §3, on pi 1.4.0) is a 90 s call that survived because it
// sent progress every 20 s, and a 300 s one that needed its own request
// timeout. So the cadence is under the default with room for one missed tick,
// and the ADR's rule is that this is read again per agent as each connector
// lands: a client whose own default is different gets an interval under it.
const progressInterval = 20 * time.Second

// newProgressTicks is production's clock: a real ticker, stopped with the
// call it reports on.
func newProgressTicks(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

// progressSender reports one call's progress for as long as that call is
// running, and never past it (nocx-a62cl.4.2, ADR-anntnxdj §3).
//
// IT DOES NOT OWN ANY LIFETIME, AND THAT IS THE POINT (ADR-8eej3e66: progress
// never renews a lifetime). A call's bound is the declaration the catalogue
// gives the tool, applied by the backend; this goroutine only writes frames
// while that call has not ended. It cannot extend the call, and nothing here
// resets anything: when the call ends — its answer, its own deadline, the
// client's cancellation — stop returns and no frame follows.
type progressSender struct {
	stopOnce sync.Once
	done     chan struct{}
	stopped  chan struct{}
}

// startProgress begins reporting a call's progress, and is a no-op for a call
// whose client sent no token: MCP's progress is opt-in per request, and a
// notification for a token nobody sent would be noise the client cannot
// attribute to anything.
//
// The notification's count is this adapter's own — one per interval, starting
// at one — because the count is a heartbeat rather than a measurement: what it
// has to be is increasing, and the total is unknown until the call ends.
func (s *Server) startProgress(ctx context.Context, writer *lineWriter, token json.RawMessage) *progressSender {
	progress := &progressSender{done: make(chan struct{}), stopped: make(chan struct{})}
	if len(bytes.TrimSpace(token)) == 0 || bytes.Equal(bytes.TrimSpace(token), []byte("null")) {
		close(progress.stopped)
		return progress
	}
	ticks, stopTicker := s.progressTicks(progressInterval)
	go func() {
		defer close(progress.stopped)
		defer stopTicker()
		var reported int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-progress.done:
				return
			case <-ticks:
				// A TICK THAT ARRIVES AS THE CALL ENDS WRITES NOTHING. Both
				// channels can be ready at once, and the half a select picks
				// is chance — so the context decides, not the select, exactly
				// as the endpoint's own cancellation does (nocx-98qho).
				if ctx.Err() != nil {
					return
				}
				reported++
				if err := writer.progress(token, reported); err != nil {
					return
				}
			}
		}
	}()
	return progress
}

// stop ends the reporting and waits for it, so no frame can follow the call's
// own answer on the session's output.
func (p *progressSender) stop() {
	p.stopOnce.Do(func() { close(p.done) })
	<-p.stopped
}

// progress writes one notifications/progress frame. The token is echoed back
// exactly as the client spelled it, which is why it is kept raw: MCP allows a
// string or an integer, and rewriting either would name a request the client
// never made.
func (w *lineWriter) progress(token json.RawMessage, reported int64) error {
	return w.write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/progress",
		"params": map[string]any{
			"progressToken": token,
			"progress":      reported,
		},
	})
}
