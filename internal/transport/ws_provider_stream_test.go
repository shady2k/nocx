package transport

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// delayedProviderBody lets the test send the body only after the handler
// starts reading it. A response before that read is an early-close race,
// independent of how fast a loopback socket happens to deliver the POST.
type delayedProviderBody struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
	eof     atomic.Bool
}

func (b *delayedProviderBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.eof.Store(true)
	}
	return n, err
}

type providerResponseOrder struct {
	*httptest.ResponseRecorder
	body      *delayedProviderBody
	responded chan struct{}
	once      sync.Once
	early     atomic.Bool
}

func (w *providerResponseOrder) WriteHeader(status int) {
	if !w.body.eof.Load() {
		w.early.Store(true)
	}
	w.once.Do(func() { close(w.responded) })
	w.ResponseRecorder.WriteHeader(status)
}

// TestProviderStreams_ConsumeRequestBeforeResponse guards the fixture's
// contract, not net/http's scheduler: the POST headers are already present,
// while its body is delivered only after the handler attempts to read it.
func TestProviderStreams_ConsumeRequestBeforeResponse(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"malformed model output": malformedReadScreen,
		"missing grant":          (&missingGrantProvider{missingID: "not-granted"}).serve,
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
			body := &delayedProviderBody{ReadCloser: reader, started: make(chan struct{})}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			req.Body = body
			req.ContentLength = 2
			req.Close = true
			req.Header.Set("Connection", "close")
			response := &providerResponseOrder{
				ResponseRecorder: httptest.NewRecorder(), body: body,
				responded: make(chan struct{}),
			}
			done := make(chan struct{})
			go func() {
				handler(response, req)
				close(done)
			}()
			select {
			case <-body.started:
				if _, err := io.WriteString(writer, `{}`); err != nil {
					t.Fatalf("send delayed POST body: %v", err)
				}
				if err := writer.Close(); err != nil {
					t.Fatalf("finish delayed POST body: %v", err)
				}
			case <-response.responded:
				// The undrained fixture responds without ever reading. Do not
				// wait for a body write that no goroutine could consume.
			}
			<-done
			if response.early.Load() || !body.eof.Load() {
				t.Fatal("provider answered before consuming the POST body; Connection: close can cut off the client's write")
			}
			if response.Code != http.StatusOK {
				t.Fatalf("provider status = %d, want 200", response.Code)
			}
		})
	}
}

// A broken POST is not a valid model request and must never get a scripted
// completion. Keep the failure visible instead of making it a tool error.
func TestProviderRequest_ReadFailureDoesNotStreamCompletion(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Body = io.NopCloser(failedProviderBody{})
	response := httptest.NewRecorder()
	malformedReadScreen(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("provider status = %d, want 400 for an unreadable request", response.Code)
	}
	if response.Header().Get("Content-Type") == "text/event-stream" {
		t.Fatal("provider streamed a completion for an unreadable request")
	}
}

type failedProviderBody struct{}

func (failedProviderBody) Read([]byte) (int, error) {
	return 0, errors.New("request body read failed")
}
