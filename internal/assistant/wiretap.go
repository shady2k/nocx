package assistant

// THE WIRE, VERBATIM.
//
// When a run goes wrong the question is always the same and until now nothing
// could answer it: what did we actually send the model, and what did it
// actually say? The structured log carries a sentence and a classified
// reason, which is right for a person and useless for this — a sentence
// cannot tell you that the tool description invited the mistake, or that the
// model answered with a tool call nobody could resolve.
//
// So this writes both halves as bytes, unedited, to a file of their own.
//
// OFF UNLESS ASKED, and off is the default: the body carries the person's
// question, the contents of whatever their tools read, and their own
// paragraph from settings. That is not something to leave lying in a file
// because it might be handy. Set NOCX_WIRE_LOG to a path to turn it on.
//
// THE KEY NEVER REACHES IT. The Authorization header and any custom header
// that could carry a credential are dropped rather than redacted-in-place,
// because a redaction is a line somebody has to keep correct and a drop is
// not.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// wireLogPath is the file the tap writes to, or "" when nobody asked for one.
// Read once at process start: a tap that could be turned on halfway through
// would make a run's record depend on when somebody set an environment
// variable.
var wireLogPath = os.Getenv("NOCX_WIRE_LOG")

// wireTap is an http.RoundTripper that copies a request and its response into
// the wire log on the way past. It changes neither.
type wireTap struct {
	inner http.RoundTripper
	mu    sync.Mutex
}

func newWireTap(inner http.RoundTripper) http.RoundTripper {
	if wireLogPath == "" {
		return inner
	}
	return &wireTap{inner: inner}
}

func (w *wireTap) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	w.write("REQUEST " + req.Method + " " + req.URL.String() + "\n" + string(body))

	resp, err := w.inner.RoundTrip(req)
	if err != nil {
		w.write("TRANSPORT ERROR\n" + err.Error())
		return resp, err
	}
	// The response is a STREAM, and the interesting part of it arrives over
	// seconds. Copying it whole here would hold the whole answer before the
	// caller saw any of it — the streaming the product is built on — so the
	// body is teed instead and each chunk lands in the log as it passes.
	w.write(fmt.Sprintf("RESPONSE %s", resp.Status))
	pr, pw := io.Pipe()
	orig := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.TeeReader(orig, pw), Closer: closerFunc(func() error {
		_ = pw.Close()
		return orig.Close()
	})}
	go func() {
		b, _ := io.ReadAll(pr)
		w.write("RESPONSE BODY\n" + string(b))
	}()
	return resp, nil
}

// write appends one record. Failures are silent: a diagnostic that could end
// a run would be worse than no diagnostic.
func (w *wireTap) write(record string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// #nosec G304 — the path is the operator's own, named in an environment
	// variable of this process. There is no untrusted input anywhere near it,
	// and the alternative (a fixed path) would put the exchange somewhere the
	// person did not choose.
	f, err := os.OpenFile(wireLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	stamp := time.Now().Format("15:04:05.000")
	_, _ = fmt.Fprintf(f, "\n===== %s %s\n%s\n", stamp, strings.SplitN(record, "\n", 2)[0], record)
}

type closerFunc func() error

func (c closerFunc) Close() error { return c() }
