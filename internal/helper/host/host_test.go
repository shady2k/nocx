package host_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// fakeService implements host.Service over a per-op params table and one
// call function, so tests can declare ops with real params types and steer
// what the handler does.
type fakeService struct {
	name   string
	ops    map[string]any
	callFn func(ctx context.Context, op string, params json.RawMessage) (any, error)
	// refuses is an optional D11 policy: the ops a cancel must be refused for.
	refuses map[string]bool
	// refusalFn is an optional RefusalCoder: codes the errors the service
	// recognises, so they cross with their code and details instead of
	// internal.
	refusalFn func(err error) (string, json.RawMessage)
}

// RefusesCancel is the optional CancelPolicy capability: false when the
// test did not declare a policy, which leaves cancellation ordinary.
func (f *fakeService) RefusesCancel(op string) bool {
	return f.refuses != nil && f.refuses[op]
}

// Refusal is the optional RefusalCoder capability: no special code when
// the test did not declare a coder.
func (f *fakeService) Refusal(err error) (string, json.RawMessage) {
	if f.refusalFn == nil {
		return "", nil
	}
	return f.refusalFn(err)
}

func (f *fakeService) Name() string { return f.name }

func (f *fakeService) Ops() []string {
	ops := make([]string, 0, len(f.ops))
	for op := range f.ops {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return ops
}

func (f *fakeService) ParamsSchema(op string) *host.Schema {
	t, ok := f.ops[op]
	if !ok {
		return nil
	}
	return host.SchemaFor(t)
}

func (f *fakeService) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	return f.callFn(ctx, op, params)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeFrame(t *testing.T, w io.Writer, ty proto.FrameType, payload []byte) {
	t.Helper()
	if _, err := w.Write(proto.EncodeFrame(ty, 0, 0, payload)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

type frame struct {
	ty      proto.FrameType
	payload []byte
}

// startReader decodes every frame the host writes to r and pushes it to the
// returned channel, so tests read responses as they arrive without racing
// the writer.
func startReader(t *testing.T, r io.Reader) <-chan frame {
	t.Helper()
	ch := make(chan frame, 16)
	go func() {
		d := proto.NewDecoder(func(ty proto.FrameType, seq, ack uint32, p []byte) {
			ch <- frame{ty: ty, payload: append([]byte(nil), p...)}
		}, func(int) {})
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				if ferr := d.Feed(buf[:n]); ferr != nil {
					return
				}
			}
			if err != nil {
				close(ch)
				return
			}
		}
	}()
	return ch
}

// readFrame waits for the next frame. Waiting on the frame itself — an
// observable state change — is the repo's timing rule: a broken host that
// can never produce a frame hangs the test, which go test's own timeout
// reports, rather than a pass depending on a duration.
func readFrame(t *testing.T, ch <-chan frame) frame {
	t.Helper()
	f, ok := <-ch
	if !ok {
		t.Fatal("output stream ended before the frame arrived")
	}
	return f
}

func readSentinel(t *testing.T, r io.Reader) {
	t.Helper()
	want := "nocx-helper " + proto.Version + " ready\n"
	got := make([]byte, 0, len(want))
	one := make([]byte, 1)
	for {
		if _, err := r.Read(one); err != nil {
			t.Fatalf("reading sentinel: %v", err)
		}
		got = append(got, one[0])
		if one[0] == '\n' {
			break
		}
	}
	if string(got) != want {
		t.Fatalf("sentinel: want %q, got %q", want, got)
	}
}

func readResponse(t *testing.T, ch <-chan frame) proto.Response {
	t.Helper()
	f := readFrame(t, ch)
	if f.ty != proto.TypeResponse {
		t.Fatalf("want a response frame, got type %v", f.ty)
	}
	var resp proto.Response
	if err := json.Unmarshal(f.payload, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

func TestHelloVersionMismatchWritesNothing(t *testing.T) {
	var out bytes.Buffer
	in := bytes.NewReader(proto.EncodeFrame(proto.TypeHello, 0, 0,
		mustJSON(proto.Hello{Version: "999", Nonce: "n"})))
	h := host.New(in, &out, "hash", "inst", slog.New(slog.NewTextHandler(io.Discard, nil)))
	err := h.Serve(context.Background())
	if !errors.Is(err, host.ErrVersionMismatch) {
		t.Fatalf("want ErrVersionMismatch, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("a mismatched hello must write nothing, wrote %q", out.Bytes())
	}
}

func TestHelloOKEchoesTheNonce(t *testing.T) {
	var out bytes.Buffer
	in := bytes.NewReader(proto.EncodeFrame(proto.TypeHello, 0, 0,
		mustJSON(proto.Hello{Version: proto.Version, Nonce: "abc123"})))
	h := host.New(in, &out, "hash", "inst", discardLogger())
	if err := h.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}

	sentinel := "nocx-helper " + proto.Version + " ready\n"
	if !bytes.HasPrefix(out.Bytes(), []byte(sentinel)) {
		t.Fatalf("want sentinel %q prefix, got %q", sentinel, out.Bytes())
	}
	var gotType proto.FrameType
	var got []byte
	d := proto.NewDecoder(func(ty proto.FrameType, seq, ack uint32, p []byte) {
		gotType, got = ty, append([]byte(nil), p...)
	}, func(int) { t.Error("unexpected gap") })
	if err := d.Feed(out.Bytes()[len(sentinel):]); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotType != proto.TypeHelloOK {
		t.Fatalf("want HelloOK after the sentinel, got %v", gotType)
	}
	var ok proto.HelloOK
	if err := json.Unmarshal(got, &ok); err != nil {
		t.Fatalf("unmarshal helloOK: %v", err)
	}
	if ok.Version != proto.Version || ok.Nonce != "abc123" || ok.ContentHash != "hash" || ok.InstanceID != "inst" {
		t.Fatalf("helloOK mismatch: %+v", ok)
	}
}

// TestGarbageBeforeHelloIsResynced feeds the real garbage a remote shell
// prints when the helper binary is missing, ahead of a valid hello. The
// decoder resyncs and the host still serves; the garbage is reported to the
// logger (stderr) and never leaks onto stdout (D22).
func TestGarbageBeforeHelloIsResynced(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	var out bytes.Buffer
	garbage := []byte("bash: nocx-helper: command not found\n")
	inBytes := append(append([]byte(nil), garbage...),
		proto.EncodeFrame(proto.TypeHello, 0, 0, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))...)
	h := host.New(bytes.NewReader(inBytes), &out, "hash", "inst", logger)
	if err := h.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	sentinel := "nocx-helper " + proto.Version + " ready\n"
	if !bytes.HasPrefix(out.Bytes(), []byte(sentinel)) {
		t.Fatalf("stdout must begin with the sentinel, got %q", out.Bytes())
	}
	if bytes.Contains(out.Bytes(), garbage) {
		t.Fatal("garbage leaked onto stdout (D22): diagnostics belong to the logger")
	}
	if !strings.Contains(logs.String(), "decoder resync") {
		t.Fatalf("the resync must be logged, logged: %q", logs.String())
	}
}

// TestASlowOperationDoesNotStallAnother is D13: two blocking handlers are
// served concurrently. The slow handler signals when it is blocked, the fast
// handler signals when it runs; the fast response is read and verified while
// the slow handler is still waiting, and only then is the slow one released.
// Every step waits on an observable event — never on a duration.
func TestASlowOperationDoesNotStallAnother(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", discardLogger())
	started := make(chan struct{})
	release := make(chan struct{})
	fastStarted := make(chan struct{})
	h.Register(&fakeService{
		name: "test",
		ops:  map[string]any{"slow": struct{}{}, "fast": struct{}{}},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			switch op {
			case "slow":
				close(started)
				<-release
				return map[string]any{"op": "slow"}, nil
			case "fast":
				close(fastStarted)
				return map[string]any{"op": "fast"}, nil
			default:
				return map[string]any{"op": op}, nil
			}
		},
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 1, Service: "test", Op: "slow", Corr: "c1"}))
	<-started // the slow handler is now blocked on release

	// The fast handler must run while the slow one is still blocked on
	// release: its started signal is the observable proof of D13, and the
	// response is verified before release is closed, so the ordering cannot
	// depend on a duration.
	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 2, Service: "test", Op: "fast", Corr: "c2"}))
	<-fastStarted
	resp := readResponse(t, outCh)
	if resp.ID != 2 {
		t.Fatalf("want the fast response (id 2) first, got id %d", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("fast response errored: %+v", resp.Error)
	}

	close(release)
	slowResp := readResponse(t, outCh)
	if slowResp.ID != 1 {
		t.Fatalf("want the slow response (id 1) after release, got id %d", slowResp.ID)
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestUnknownServiceAndOpDoNotCloseTheConnection(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", discardLogger())
	h.Register(&fakeService{
		name: "test",
		ops:  map[string]any{"known": struct{}{}},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			return map[string]any{"op": op}, nil
		},
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 1, Service: "nope", Op: "x"}))
	resp := readResponse(t, outCh)
	if resp.ID != 1 || resp.Error == nil || resp.Error.Code != proto.ErrCodeUnknownService {
		t.Fatalf("want unknown service, got %+v", resp)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 2, Service: "test", Op: "nope"}))
	resp = readResponse(t, outCh)
	if resp.ID != 2 || resp.Error == nil || resp.Error.Code != proto.ErrCodeUnknownOp {
		t.Fatalf("want unknown op, got %+v", resp)
	}

	// The connection is still alive: a third request is served and answered.
	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 3, Service: "test", Op: "known", Corr: "c3"}))
	resp = readResponse(t, outCh)
	if resp.ID != 3 || resp.Error != nil {
		t.Fatalf("want the known op answered, got %+v", resp)
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestBadParamsAnswersErrCodeBadParams(t *testing.T) {
	type statusParams struct {
		Path string `json:"path"`
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", discardLogger())
	h.Register(&fakeService{
		name: "git",
		ops:  map[string]any{"status": statusParams{}},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			return map[string]any{"op": op}, nil
		},
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	// The params are shape-invalid on purpose: an array where the op's
	// schema declares a struct. The envelope parses, so the host can answer
	// with an id; the schema decode is what refuses.
	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{
		ID: 1, Service: "git", Op: "status", Params: json.RawMessage(`[1,2,3]`),
	}))
	resp := readResponse(t, outCh)
	if resp.ID != 1 || resp.Error == nil || resp.Error.Code != proto.ErrCodeBadParams {
		t.Fatalf("want bad params, got %+v", resp)
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// TestCancelReachesTheRequestContext exercises the per-request context the
// host stores by id: a TypeCancel naming the request unblocks a handler that
// waits on its context. The handler's response arriving after the cancel is
// the proof the cancel reached it.
func TestCancelReachesTheRequestContext(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", discardLogger())
	started := make(chan struct{})
	h.Register(&fakeService{
		name: "test",
		ops:  map[string]any{"wait": struct{}{}},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 7, Service: "test", Op: "wait"}))
	<-started // the handler is now blocked on its context

	writeFrame(t, inW, proto.TypeCancel, mustJSON(struct {
		ID uint64 `json:"id"`
	}{ID: 7}))
	resp := readResponse(t, outCh)
	if resp.ID != 7 {
		t.Fatalf("want the cancelled request's response (id 7), got id %d", resp.ID)
	}
	if resp.Error == nil {
		t.Fatal("want the cancelled handler to answer its cancellation, got success")
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// TestAGenerationWithoutASessionServiceStillAnswers is what remains of D15's
// reservation now that nocx-k6p18.3 has cashed it in. The name is no longer
// refused at registration — internal/helper/session owns it — but a build
// composed WITHOUT that service is not a broken build: it answers the name
// like any other unknown service rather than panicking, hanging or closing the
// connection. Generations coexist for months, so a coordinator that knows
// about sessions WILL reach a helper that does not.
func TestAGenerationWithoutASessionServiceStillAnswers(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "h", "i", discardLogger())
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 1, Service: proto.ServiceSession, Op: proto.OpSpawn}))
	resp := readResponse(t, outCh)
	if resp.ID != 1 || resp.Error == nil || resp.Error.Code != proto.ErrCodeUnknownService {
		t.Fatalf("want a request for session answered unknown service, got %+v", resp)
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// TestOneServiceOwnsOneName is the rule the reserved name existed to protect,
// generalised now that the reservation is spent. serviceByName answers with
// the FIRST match, so a second service registered under one name is not a
// conflict anybody would ever see — it is a service that silently never runs,
// which is the worst of the three possible outcomes.
func TestOneServiceOwnsOneName(t *testing.T) {
	h := host.New(nil, io.Discard, "h", "i", discardLogger())
	h.Register(&fakeService{name: "twice", ops: map[string]any{"op": struct{}{}}})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("want Register to panic on a duplicate service name")
		}
	}()
	h.Register(&fakeService{name: "twice", ops: map[string]any{"other": struct{}{}}})
}

// TestEveryRequestLogsItsCorr pins D26: the log line the host emits for a
// request carries the request's Corr, so a trace can be followed across the
// wire.
func TestEveryRequestLogsItsCorr(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", logger)
	h.Register(&fakeService{
		name: "test",
		ops:  map[string]any{"known": struct{}{}},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			return map[string]any{"op": op}, nil
		},
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 1, Service: "test", Op: "known", Corr: "corr-xyz"}))
	if resp := readResponse(t, outCh); resp.ID != 1 || resp.Error != nil {
		t.Fatalf("want the known op answered, got %+v", resp)
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !strings.Contains(logs.String(), "corr-xyz") {
		t.Fatalf("the request log line must carry the corr, logged: %q", logs.String())
	}
}

// TestNoOperationAcceptsArgv is D3 with teeth. An operation whose params carry
// a list of strings destined for a command line turns this helper into a
// remote shell, and the closed set of named operations into a fiction. orca
// kept exactly one such operation and paid for it with a 300-line allowlist
// validator; we keep none, and this test is why that stays true.
func TestNoOperationAcceptsArgv(t *testing.T) {
	h := host.New(nil, io.Discard, "h", "i", discardLogger())
	h.Register(&fakeService{
		name: "git",
		ops: map[string]any{
			"status": struct{}{},
			"log":    struct{}{},
		},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			return nil, nil
		},
	})
	for _, svc := range h.Services() {
		for _, op := range svc.Ops() {
			schema := svc.ParamsSchema(op) // every op declares its params type
			for _, field := range schema.Fields() {
				if field.IsFreeFormStringList() {
					t.Errorf("%s.%s takes %q: no operation may accept argv (D3)",
						svc.Name(), op, field.Name)
				}
			}
		}
	}
}

// TestIsFreeFormStringListDiscriminates proves the D3 detector can tell the
// one legal string list — a pathspec, tagged nocx:"pathspec" (D8) — from a
// bare []string, and that only that tag matters. Without this, a vacuous D3
// test could not tell the checker from a no-op.
func TestIsFreeFormStringListDiscriminates(t *testing.T) {
	type pathspecParams struct {
		Paths []string `nocx:"pathspec"`
	}
	type freeParams struct {
		Args []string
	}
	type otherParams struct {
		Name string
	}
	tests := []struct {
		name   string
		schema *host.Schema
		want   bool
	}{
		{"pathspec is not free-form", host.SchemaFor(pathspecParams{}), false},
		{"bare string slice is free-form", host.SchemaFor(freeParams{}), true},
		{"a json tag does not save it", host.SchemaFor(struct {
			Args []string `json:"args"`
		}{}), true},
		{"scalar is not a list", host.SchemaFor(otherParams{}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := false
			for _, f := range tt.schema.Fields() {
				if f.IsFreeFormStringList() {
					got = true
				}
			}
			if got != tt.want {
				t.Fatalf("IsFreeFormStringList = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRegisterPanicsOnFreeFormList pins D3 as enforced rather than tested:
// the registration path itself refuses a service whose op declares a
// free-form string list, so the rule cannot be forgotten by a future
// service author.
func TestRegisterPanicsOnFreeFormList(t *testing.T) {
	type argvParams struct {
		Args []string
	}
	h := host.New(nil, io.Discard, "h", "i", discardLogger())
	svc := &fakeService{
		name: "test",
		ops:  map[string]any{"run": argvParams{}},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			return nil, nil
		},
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("want Register to panic for a free-form string list (D3)")
		}
	}()
	h.Register(svc)
}

// schemaLessService claims an op in Ops() but declares no params type for
// it — the service-author bug Register must refuse.
type schemaLessService struct {
	fakeService
	ops []string
}

func (s *schemaLessService) Ops() []string {
	return s.ops
}

func (s *schemaLessService) ParamsSchema(op string) *host.Schema {
	return nil
}

// TestRegisterPanicsOnMissingSchema pins the counterpart of "every op
// declares its params type": an op in Ops() with no schema is a service bug
// the dispatcher would otherwise answer as an unknown op.
func TestRegisterPanicsOnMissingSchema(t *testing.T) {
	h := host.New(nil, io.Discard, "h", "i", discardLogger())
	svc := &schemaLessService{fakeService: fakeService{name: "test"}, ops: []string{"ghost"}}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("want Register to panic for an op with no params schema")
		}
	}()
	h.Register(svc)
}

// bigHost starts a host over fresh pipes serving one "big" op that returns
// the given payload, completes the hello handshake, and returns the frame
// channel, the request-side writer and the serve result channel.
func bigHost(t *testing.T, payload string) (<-chan frame, io.WriteCloser, <-chan error) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", discardLogger())
	h.Register(&fakeService{
		name: "test",
		ops:  map[string]any{"big": struct{}{}},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			return map[string]any{"data": payload}, nil
		},
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}
	return outCh, inW, serveDone
}

// TestChunkingPinsTheFrameBoundary is D14's wire half at the REAL bound: a
// response one byte above proto.MaxFrameBytes is sent as a ChunkedResult
// sentinel followed by TypeChunk frames whose payloads concatenate to the
// original result, and a response one byte below it is one ordinary frame.
// The sizes are derived from the marshalled payloads, so an off-by-one in
// the constant or the threshold fails the test — a test at a lowered bound
// could prove the logic at 512 bytes and still ship a wrong production
// constant.
func TestChunkingPinsTheFrameBoundary(t *testing.T) {
	// overhead is the marshaled bytes a response carries around the result
	// string: the response envelope, the result object and the string's
	// quotes. Measured from a zero-length probe so the boundary sizes below
	// are exact for this payload shape, never hardcoded.
	overhead := len(mustJSON(proto.Response{ID: 7, Result: json.RawMessage(`{"data":""}`)}))

	t.Run("one byte below the bound is one frame", func(t *testing.T) {
		payload := strings.Repeat("x", proto.MaxFrameBytes-overhead-1)
		outCh, inW, serveDone := bigHost(t, payload)
		writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 7, Service: "test", Op: "big", Corr: "c"}))

		f := readFrame(t, outCh)
		if f.ty != proto.TypeResponse {
			t.Fatalf("below the bound must be one response frame, got %v", f.ty)
		}
		want := mustJSON(proto.Response{ID: 7, Result: mustJSON(map[string]any{"data": payload})})
		if len(want) != proto.MaxFrameBytes-1 {
			t.Fatalf("fixture response is %d bytes, want %d — the boundary case is not pinned", len(want), proto.MaxFrameBytes-1)
		}
		if !bytes.Equal(f.payload, want) {
			t.Fatalf("response below the bound differs from the expected single frame: %d vs %d bytes", len(f.payload), len(want))
		}

		_ = inW.Close()
		if err := <-serveDone; err != nil {
			t.Fatalf("serve: %v", err)
		}
	})

	t.Run("one byte above the bound is chunked", func(t *testing.T) {
		payload := strings.Repeat("x", proto.MaxFrameBytes-overhead+1)
		outCh, inW, serveDone := bigHost(t, payload)
		writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 7, Service: "test", Op: "big", Corr: "c"}))

		f := readFrame(t, outCh)
		if f.ty != proto.TypeResponse {
			t.Fatalf("want the ChunkedResult sentinel first, got %v", f.ty)
		}
		// Prove the payload really straddles the threshold: the response
		// the host would have marshalled un-chunked is one byte over the
		// frame bound, so the chunking below is the production path doing
		// its job, not a smaller bound.
		rawAbove := mustJSON(proto.Response{ID: 7, Result: mustJSON(map[string]any{"data": payload})})
		if len(rawAbove) != proto.MaxFrameBytes+1 {
			t.Fatalf("fixture response is %d bytes, want %d — the boundary case is not pinned", len(rawAbove), proto.MaxFrameBytes+1)
		}
		var sentinel proto.Response
		if err := json.Unmarshal(f.payload, &sentinel); err != nil {
			t.Fatalf("unmarshal sentinel: %v", err)
		}
		if sentinel.ID != 7 {
			t.Fatalf("sentinel id = %d, want 7", sentinel.ID)
		}
		var cr proto.ChunkedResult
		if err := json.Unmarshal(sentinel.Result, &cr); err != nil {
			t.Fatalf("unmarshal ChunkedResult: %v", err)
		}
		if cr.ChunkCount < 2 {
			t.Fatalf("a payload one byte over the bound must split into at least two chunks, got %d", cr.ChunkCount)
		}

		var got []byte
		for range cr.ChunkCount {
			f := readFrame(t, outCh)
			if f.ty != proto.TypeChunk {
				t.Fatalf("want a TypeChunk frame, got %v", f.ty)
			}
			if len(f.payload) > proto.MaxFrameBytes {
				t.Fatalf("chunk frame payload is %d bytes, over the %d-byte bound", len(f.payload), proto.MaxFrameBytes)
			}
			var ch proto.Chunk
			if err := json.Unmarshal(f.payload, &ch); err != nil {
				t.Fatalf("unmarshal chunk: %v", err)
			}
			if ch.ChunkedStreamID != cr.ChunkedStreamID {
				t.Fatalf("chunk stream = %d, want %d", ch.ChunkedStreamID, cr.ChunkedStreamID)
			}
			got = append(got, ch.Bytes...)
		}
		want := mustJSON(map[string]any{"data": payload})
		if !bytes.Equal(got, want) {
			t.Fatalf("chunks do not concatenate to the original result: %d vs %d bytes", len(got), len(want))
		}
		if len(got) != cr.TotalBytes {
			t.Fatalf("reassembled %d bytes, sentinel promised %d", len(got), cr.TotalBytes)
		}

		_ = inW.Close()
		if err := <-serveDone; err != nil {
			t.Fatalf("serve: %v", err)
		}
	})
}

// TestCancelRefusedWhenTheServiceDeclaresIt is D11's mechanism at the
// host: a service that declares an op refuses cancellation is answered
// with ErrCodeCancelRefused — a refusal is a fact the caller can act on, a
// no-op looks like success — and the operation runs to completion. The
// handler's result arriving after the refusal is the proof it was never
// stopped.
func TestCancelRefusedWhenTheServiceDeclaresIt(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", discardLogger())
	started := make(chan struct{})
	release := make(chan struct{})
	h.Register(&fakeService{
		name:    "test",
		ops:     map[string]any{"mutate": struct{}{}},
		refuses: map[string]bool{"mutate": true},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			close(started)
			<-release
			return "done", nil
		},
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 7, Service: "test", Op: "mutate"}))
	<-started // the mutation is now in flight

	writeFrame(t, inW, proto.TypeCancel, mustJSON(struct {
		ID uint64 `json:"id"`
	}{ID: 7}))
	refusal := readResponse(t, outCh)
	if refusal.ID != 7 {
		t.Fatalf("want the refused request's id (7), got %d", refusal.ID)
	}
	if refusal.Error == nil || refusal.Error.Code != proto.ErrCodeCancelRefused {
		t.Fatalf("want a cancel_refused refusal, got %+v", refusal.Error)
	}

	close(release)
	resp := readResponse(t, outCh)
	if resp.Error != nil {
		t.Fatalf("the mutation must complete despite the refused cancel: %+v", resp.Error)
	}
	if resp.ID != 7 {
		t.Fatalf("want the mutation's own response (id 7), got %d", resp.ID)
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// TestServiceErrorCrossesWithCodeAndDetails pins the RefusalCoder
// mechanism: a service that codes one of its errors sees the code and the
// structured details cross on the wire, so the backend can rebuild the
// typed error — fields intact — instead of seeing an opaque internal
// failure. An uncoded error still crosses as internal.
func TestServiceErrorCrossesWithCodeAndDetails(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", discardLogger())
	h.Register(&fakeService{
		name: "test",
		ops:  map[string]any{"coded": struct{}{}, "plain": struct{}{}},
		refusalFn: func(err error) (string, json.RawMessage) {
			if err.Error() == "the coded failure" {
				return "git.conflicted", mustJSON(struct {
					Path string `json:"path"`
				}{Path: "f.txt"})
			}
			return "", nil
		},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			if op == "coded" {
				return nil, errors.New("the coded failure")
			}
			return nil, errors.New("the plain failure")
		},
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 1, Service: "test", Op: "coded"}))
	coded := readResponse(t, outCh)
	if coded.Error == nil || coded.Error.Code != "git.conflicted" {
		t.Fatalf("want the coded refusal, got %+v", coded.Error)
	}
	if !strings.Contains(string(coded.Error.Details), `"f.txt"`) {
		t.Fatalf("the details must carry the structured path, got %s", coded.Error.Details)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 2, Service: "test", Op: "plain"}))
	plain := readResponse(t, outCh)
	if plain.Error == nil || plain.Error.Code != proto.ErrCodeInternal {
		t.Fatalf("an uncoded error must stay internal, got %+v", plain.Error)
	}
	if len(plain.Error.Details) != 0 {
		t.Fatalf("an uncoded error carries no details, got %s", plain.Error.Details)
	}

	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// eofSeenReader is an io.Reader that announces the moment the read loop has
// consumed the EOF, so a test never has to guess when Serve stopped reading.
// Without it the only way to know is to wait a while, which is the timing
// dependency this repository forbids.
type eofSeenReader struct {
	r    io.Reader
	once sync.Once
	seen chan struct{}
}

func newEOFSeenReader(r io.Reader) *eofSeenReader {
	return &eofSeenReader{r: r, seen: make(chan struct{})}
}

func (e *eofSeenReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		e.once.Do(func() { close(e.seen) })
	}
	return n, err
}

// TestServeWaitsForItsHandlersBeforeReturning is the closing end of D13's
// interval. D13 buys concurrency: `frame` dispatches every request on its own
// goroutine so a blocking handler cannot stall the read loop. That opens an
// interval — a handler is running while the loop moves on — and nothing
// closed it: Serve returned the moment `in` reached EOF, with handlers still
// executing.
//
// The consequence is not academic. cmd/nocx-helper's main returns as soon as
// Serve does, so on a real remote host a transport that dies mid-mutation
// took the helper process down with the `git commit` it had spawned still
// writing into .git — the half-written repository D12 exists to prevent. In
// the tests it showed up as internal/git/helper failing in t.TempDir's
// RemoveAll while every assertion passed (nocx-x2e53, nocx-t76b9): those
// tests wait on the peer's exit, and the peer's exit did not mean what its
// comment claimed it meant.
//
// So: Serve has returned implies no handler is still running.
//
// The bound below gates only a false GREEN, never a red. In a correct host
// Serve CANNOT return while the handler sits on `release`, whatever the
// machine is doing, so no bound of any length can make this test flaky; a
// machine slow enough to miss a broken host's return within two seconds
// would have to stall two statements for that long.
func TestServeWaitsForItsHandlersBeforeReturning(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	in := newEOFSeenReader(inR)
	h := host.New(in, outW, "hash", "inst", discardLogger())

	started := make(chan struct{})
	release := make(chan struct{})
	var handlerDone atomic.Bool
	h.Register(&fakeService{
		name: "test",
		ops:  map[string]any{"slow": struct{}{}},
		// A mutation: it refuses cancellation (D11), so the dead transport
		// cannot abandon it and Serve has nothing to do but wait.
		refuses: map[string]bool{"slow": true},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			close(started)
			<-release
			handlerDone.Store(true)
			return map[string]any{"op": op}, nil
		},
	})

	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 1, Service: "test", Op: "slow", Corr: "c1"}))
	<-started // the handler is now inside Call, holding the request open

	_ = inW.Close() // the transport dies mid-request
	<-in.seen       // and the read loop has taken the EOF: only the handler can hold Serve now

	select {
	case err := <-serveDone:
		t.Fatalf("Serve returned while its handler was still running (err %v): a helper process would exit here, killing the git it spawned mid-write", err)
	case <-time.After(2 * time.Second):
	}

	close(release)
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !handlerDone.Load() {
		t.Fatal("Serve returned before its handler finished")
	}
}

// TestADeadTransportAbandonsWhatMayBeCancelled is the other end of the same
// change. Waiting for every handler would trade a half-written repository for
// a helper process that never exits: a read still running when the transport
// dies has nobody left to answer it. So a request whose service does not
// refuse cancellation has its context cancelled when the read loop ends, and
// only the refusing half — the mutations of D11 — is waited for.
func TestADeadTransportAbandonsWhatMayBeCancelled(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := host.New(inR, outW, "hash", "inst", discardLogger())

	started := make(chan struct{})
	var sawCancel atomic.Bool
	h.Register(&fakeService{
		name: "test",
		ops:  map[string]any{"read": struct{}{}},
		callFn: func(ctx context.Context, op string, params json.RawMessage) (any, error) {
			close(started)
			<-ctx.Done() // the observable: nothing else can release this handler
			sawCancel.Store(true)
			return nil, ctx.Err()
		},
	})

	serveDone := make(chan error, 1)
	go func() { serveDone <- h.Serve(context.Background()) }()

	writeFrame(t, inW, proto.TypeHello, mustJSON(proto.Hello{Version: proto.Version, Nonce: "n"}))
	readSentinel(t, outR)
	outCh := startReader(t, outR)
	if f := readFrame(t, outCh); f.ty != proto.TypeHelloOK {
		t.Fatalf("want HelloOK, got %v", f.ty)
	}

	writeFrame(t, inW, proto.TypeRequest, mustJSON(proto.Request{ID: 1, Service: "test", Op: "read", Corr: "c1"}))
	<-started

	// Nothing ever closes a release channel here: if the dead transport did
	// not cancel the handler, Serve waits for it forever and go test's own
	// timeout reports that, rather than a duration deciding the result.
	_ = inW.Close()
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !sawCancel.Load() {
		t.Fatal("the handler was never cancelled")
	}
}
