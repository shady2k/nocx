package update

// THE DOWNLOAD HAS NO TOTAL BOUND (nocx-vh2hr.1.15).
//
// `&http.Client{Timeout: 30 * time.Second}` bounds the WHOLE request, body
// included, so a 150 MB AppImage on an ordinary connection was cut off at thirty
// seconds — a nightly that could not update itself on the machine it was built
// for. What a download needs instead is a bound on each PHASE that can hang
// (connect, TLS, the server's first byte) and a STALL timer on the body, which
// says "no bytes for N seconds" rather than "too slow overall".
//
// The manifest fetch keeps its own small client: it is a few kilobytes, and a
// total bound there is the right rule.

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Phase timeouts. Each is a bound on a silence, never on the whole transfer.
const (
	downloadDialTimeout    = 30 * time.Second
	downloadTLSTimeout     = 15 * time.Second
	downloadHeaderTimeout  = 30 * time.Second
	downloadStallTimeout   = 60 * time.Second
	downloadStallGraceTick = 5 * time.Second
)

// ErrDownloadStalled is the named refusal of a body that stopped arriving.
// It is its own error so a caller (and a test) can tell a stall from a broken
// connection, and so the surface can name the step.
var ErrDownloadStalled = errors.New("download stalled: the server stopped sending bytes")

// DownloadClient is the client the artefact download uses: phase timeouts and
// no total one.
func DownloadClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   downloadDialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   downloadTLSTimeout,
			ResponseHeaderTimeout: downloadHeaderTimeout,
			ExpectContinueTimeout: time.Second,
			// A resumed range and a fresh body are the same connection pool;
			// nothing here bounds the body itself.
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// stallReader fails a read that waits longer than the stall window for bytes.
//
// One goroutine per read, because net/http's body read has no deadline of its
// own; the read it may block in is abandoned when the timer fires, and the
// caller closes the body, which is what frees it. The timer is reset by BYTES,
// not by time: a slow server that keeps sending is never a stall.
type stallReader struct {
	body  io.Reader
	stall time.Duration
}

func (s *stallReader) Read(p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := s.body.Read(p)
		ch <- result{n, err}
	}()
	timer := time.NewTimer(s.stall)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.n, r.err
	case <-timer.C:
		return 0, fmt.Errorf("%w: no bytes for %s", ErrDownloadStalled, s.stall)
	}
}
