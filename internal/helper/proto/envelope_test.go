package proto

import (
	"encoding/json"
	"testing"
)

// TestEnvelopeWireShapes pins the JSON wire contract: field names are exact
// and lowerCamel (contentHash, instanceId, chunkedStreamId, totalBytes,
// chunkCount), and the optional halves (params, result, error) are omitted
// rather than marshalled as null when absent.
func TestEnvelopeWireShapes(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{
			"hello",
			Hello{Version: "1", Nonce: "abc", Corr: "c1"},
			`{"version":"1","nonce":"abc","corr":"c1"}`,
		},
		{
			"helloOK",
			HelloOK{Version: "1", Nonce: "abc", ContentHash: "h", InstanceID: "i"},
			`{"version":"1","nonce":"abc","contentHash":"h","instanceId":"i"}`,
		},
		{
			"request",
			Request{ID: 9, Service: "git", Op: "status", Params: json.RawMessage(`{"path":"/x"}`), Corr: "c"},
			`{"id":9,"service":"git","op":"status","params":{"path":"/x"},"corr":"c"}`,
		},
		{
			"requestNoParams",
			Request{ID: 9, Service: "git", Op: "status", Corr: "c"},
			`{"id":9,"service":"git","op":"status","corr":"c"}`,
		},
		{
			"responseResult",
			Response{ID: 9, Result: json.RawMessage(`{"ok":true}`)},
			`{"id":9,"result":{"ok":true}}`,
		},
		{
			"responseError",
			Response{ID: 9, Error: &Error{Code: ErrCodeUnknownOp, Message: "no such op"}},
			`{"id":9,"error":{"code":"unknown_op","message":"no such op"}}`,
		},
		{
			"chunkedResult",
			ChunkedResult{ChunkedStreamID: 3, TotalBytes: 1024, ChunkCount: 2},
			`{"chunkedStreamId":3,"totalBytes":1024,"chunkCount":2}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("wire shape: want %s, got %s", tt.want, got)
			}
		})
	}
}

// TestEnvelopeRoundTrip decodes the wire shapes back into the Go types,
// proving the field names above are readable by the receiver too.
func TestEnvelopeRoundTrip(t *testing.T) {
	req := Request{ID: 42, Service: "git", Op: "log", Params: json.RawMessage(`{"n":5}`), Corr: "corr-1"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Request
	if err = json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ID != req.ID || back.Service != req.Service || back.Op != req.Op || back.Corr != req.Corr ||
		string(back.Params) != string(req.Params) {
		t.Fatalf("round trip: got %+v, want %+v", back, req)
	}

	resp := Response{ID: 42, Error: &Error{Code: ErrCodeInternal, Message: "boom"}}
	data, err = json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var respBack Response
	if err = json.Unmarshal(data, &respBack); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if respBack.Error == nil || respBack.Error.Code != ErrCodeInternal || respBack.Error.Message != "boom" {
		t.Fatalf("response round trip: got %+v", respBack)
	}
}

// TestErrorCodesAreDistinct guards the closed set of protocol error codes:
// the host and the client both switch on these strings, so two codes
// colliding would silently misroute an error.
func TestErrorCodesAreDistinct(t *testing.T) {
	codes := []string{
		ErrCodeUnknownService,
		ErrCodeUnknownOp,
		ErrCodeCancelRefused,
		ErrCodeBadParams,
		ErrCodeInternal,
	}
	for i, a := range codes {
		if a == "" {
			t.Fatalf("error code %d is empty", i)
		}
		for j, b := range codes {
			if i != j && a == b {
				t.Fatalf("error codes %d and %d collide: %q", i, j, a)
			}
		}
	}
}
