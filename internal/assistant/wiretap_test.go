package assistant

// The tap has to be provable, because a diagnostic nobody has watched work is
// the thing you reach for on the day it is wrong. Two facts: both halves of
// the exchange land in the file, and the API key does not.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

func TestWireTap_RecordsBothHalvesAndNeverTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wire.log")
	t.Setenv("NOCX_WIRE_LOG", path)

	grant, dir := testDirGrant(t, autonomousMatrix())
	file := filepath.Join(dir, "a.txt")
	writeFile(t, file, "wire tap fixture")
	args, err := json.Marshal(map[string]string{"path": file})
	if err != nil {
		t.Fatalf("marshal files.read args: %v", err)
	}
	_, srv := newFakeOpenAI(callThenAnswer(toolCallSpec{
		name: "files.read",
		args: string(args),
	}))
	defer srv.Close()
	cl, _ := newClient(nil, os.DirFS(realToolsFS), nil, content.NoFloor())
	p := askParams(srv.URL, &grant, &fakeLedger{}, NewApprovalStore())
	if askErr := cl.Ask(context.Background(), p, func(AskEvent) error { return nil }); askErr != nil {
		t.Fatalf("Ask: %v", askErr)
	}
	b, err := os.ReadFile(path) // #nosec G304 — the test wrote this path itself
	if err != nil {
		t.Fatalf("the tap wrote nothing: %v", err)
	}
	got := string(b)
	for _, want := range []string{"REQUEST POST", "files.read", "RESPONSE 200", "RESPONSE BODY", "chat.completion.chunk"} {
		if !strings.Contains(got, want) {
			t.Fatalf("wire log lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sk-test-123") {
		t.Fatalf("THE KEY IS IN THE WIRE LOG:\n%s", got)
	}
	t.Logf("wire log (%d bytes) starts:\n%s", len(got), got[:min(1200, len(got))])
}

type wireTestRecord struct {
	kind      string
	body      []byte
	truncated bool
}

type wireTestRecorder struct {
	records []wireTestRecord
}

func (r *wireTestRecorder) RecordWire(_ context.Context, _, _, kind string, body []byte, truncated bool) {
	r.records = append(r.records, wireTestRecord{kind: kind, body: append([]byte(nil), body...), truncated: truncated})
}

func TestWireTap_CapsCaptureWithoutTruncatingProviderRequest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		truncated bool
	}{
		{name: "below cap", size: wireCaptureCap - 1},
		{name: "at cap", size: wireCaptureCap},
		{name: "above cap", size: wireCaptureCap + 1, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := bytes.Repeat([]byte{'r'}, tc.size)
			var gotRequest []byte
			inner := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				var err error
				gotRequest, err = io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				return &http.Response{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("response")),
				}, nil
			})
			recorder := &wireTestRecorder{}
			tap := newWireTapWith(inner, "", recorder)
			req, err := http.NewRequestWithContext(
				WithWireIdentity(context.Background(), "run-1", "entry-1"),
				http.MethodPost, "https://example.test/v1", bytes.NewReader(want),
			)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := tap.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if !bytes.Equal(gotRequest, want) {
				t.Fatalf("inner received %d bytes, want %d", len(gotRequest), len(want))
			}
			if len(recorder.records) < 2 {
				t.Fatalf("records = %d, want request and response", len(recorder.records))
			}
			request := recorder.records[0]
			if request.kind != "request" || request.truncated != tc.truncated || len(request.body) != minInt(tc.size, wireCaptureCap) {
				t.Fatalf("request record = kind %q, bytes %d, truncated %v", request.kind, len(request.body), request.truncated)
			}
			if recorder.records[1].kind != "response" {
				t.Fatalf("second record kind = %q, want response", recorder.records[1].kind)
			}
		})
	}
}

func TestWireTap_ResponseCapDistinguishesEOFFromEarlyClose(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		readSize  int
		truncated bool
	}{
		{name: "below cap EOF", size: wireCaptureCap - 1, readSize: wireCaptureCap - 1},
		{name: "at cap EOF", size: wireCaptureCap, readSize: wireCaptureCap},
		{name: "above cap EOF", size: wireCaptureCap + 1, readSize: wireCaptureCap + 1, truncated: true},
		{name: "early close", size: wireCaptureCap, readSize: 1, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &wireTestRecorder{}
			inner := roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					Status:     "200 OK",
					StatusCode: http.StatusOK,
					Body: io.NopCloser(bytes.NewReader(bytes.Repeat(
						[]byte{'s'}, tc.size,
					))),
				}, nil
			})
			tap := newWireTapWith(inner, "", recorder)
			req, err := http.NewRequestWithContext(
				WithWireIdentity(context.Background(), "run-1", "entry-1"),
				http.MethodGet, "https://example.test/v1", nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := tap.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			if tc.readSize == tc.size {
				_, err = io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				buf := make([]byte, tc.readSize)
				if _, err := io.ReadFull(resp.Body, buf); err != nil {
					t.Fatal(err)
				}
			}
			_ = resp.Body.Close()
			if len(recorder.records) != 2 {
				t.Fatalf("records = %d, want request and response", len(recorder.records))
			}
			got := recorder.records[1]
			if got.kind != "response" || got.truncated != tc.truncated || len(got.body) != minInt(tc.readSize, wireCaptureCap) {
				t.Fatalf("response record = kind %q, bytes %d, truncated %v", got.kind, len(got.body), got.truncated)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
