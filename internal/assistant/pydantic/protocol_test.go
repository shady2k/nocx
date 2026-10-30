package pydantic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/assistant"
)

func wireJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFramesRejectInvalidIdentityAndBounds(t *testing.T) {
	id := "g:1"
	valid := envelope{BridgeVersion, "hello", "operation", "epoch", 1, &id, wireJSON(t, helloPayload{strings.Repeat("a", 64), 1})}
	var buffer bytes.Buffer
	if err := writeEnvelope(&buffer, valid); err != nil {
		t.Fatal(err)
	}
	got, err := readEnvelope(&buffer)
	if err != nil || got.OperationID != "operation" {
		t.Fatalf("round trip: %v %#v", err, got)
	}
	for _, size := range []uint32{0, maxFrame + 1, 1 << 31} {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], size)
		if _, err := readEnvelope(bytes.NewReader(prefix[:])); !errors.Is(err, errProtocol) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	for _, payload := range []string{`{"manifestDigest":"a","bridgeVersion":1,"unexpected":true}`, `{"manifestDigest":"a"}`, `null`, `{"manifestDigest":"a","bridgeVersion":1} {}`} {
		var hello helloPayload
		if err := decodeStrict([]byte(payload), &hello); err == nil {
			t.Fatalf("accepted %s", payload)
		}
	}
	p := &process{pending: map[string]string{"g:1": "hello"}, incoming: make(map[string]bool)}
	if err := p.validate(valid); err != nil {
		t.Fatal(err)
	}
	if err := p.validate(valid); !errors.Is(err, errProtocol) {
		t.Fatalf("reused reply: %v", err)
	}
	for _, request := range []string{"g:2", "p:0", "p:01"} {
		e := valid
		e.Kind = "tool.invoke"
		e.RequestID = &request
		if err := p.validate(e); !errors.Is(err, errProtocol) {
			t.Fatalf("namespace %s: %v", request, err)
		}
	}
	request := "p:1"
	valid.Kind = "tool.invoke"
	valid.RequestID = &request
	if err := p.validate(valid); err != nil {
		t.Fatal(err)
	}
	if err := p.validate(valid); !errors.Is(err, errProtocol) {
		t.Fatalf("reused request: %v", err)
	}
}

func TestObjectCommitPublishesOnlyCompleteVerifiedPayload(t *testing.T) {
	data := wireJSON(t, textPayload{strings.Repeat("λ", maxFrame)})
	id := "g:3"
	digest := sha256.Sum256(data)
	build := func(t *testing.T, mutate func(*objectCommit)) error {
		t.Helper()
		var objects objectReader
		ordinal := 0
		for offset := 0; offset < len(data); offset += chunkBytes {
			chunk := objectChunk{"p:o:1", "generated", &id, len(data), ordinal, data[offset:min(offset+chunkBytes, len(data))]}
			got, err := objects.accept(envelope{Kind: "object.chunk", Payload: wireJSON(t, chunk)})
			if err != nil {
				return err
			}
			if got != nil {
				t.Fatal("partial object was published")
			}
			ordinal++
		}
		commit := objectCommit{"p:o:1", "generated", &id, len(data), ordinal, hex.EncodeToString(digest[:])}
		if mutate != nil {
			mutate(&commit)
		}
		got, err := objects.accept(envelope{Kind: "object.commit", Payload: wireJSON(t, commit)})
		if err == nil && (!bytes.Equal(got.Payload, data) || got.Kind != "generated" || *got.RequestID != id) {
			t.Fatal("committed object changed")
		}
		return err
	}
	if err := build(t, nil); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*objectCommit){func(c *objectCommit) { c.SHA256 = strings.Repeat("0", 64) }, func(c *objectCommit) { c.TotalBytes-- }, func(c *objectCommit) { c.Chunks++ }, func(c *objectCommit) { c.Kind = "tool.result" }} {
		if err := build(t, mutate); !errors.Is(err, errProtocol) {
			t.Fatalf("corrupt commit: %v", err)
		}
	}
	var objects objectReader
	oversized := objectChunk{"p:o:2", "generated", &id, maxObject + 1, 0, []byte("x")}
	if _, err := objects.accept(envelope{Kind: "object.chunk", Payload: wireJSON(t, oversized)}); !errors.Is(err, errProtocol) {
		t.Fatalf("oversize: %v", err)
	}
}

func TestLargeOutgoingObjectUsesBoundedFrames(t *testing.T) {
	var buffer bytes.Buffer
	p := &process{input: bufferCloser{&buffer}, operationID: "op", epoch: "epoch"}
	id := "g:1"
	want := textPayload{strings.Repeat("x", 2*maxFrame)}
	if err := p.send("generated", &id, want); err != nil {
		t.Fatal(err)
	}
	var objects objectReader
	var result *envelope
	for buffer.Len() > 0 {
		frame, err := readEnvelope(&buffer)
		if err != nil {
			t.Fatal(err)
		}
		result, err = objects.accept(frame)
		if err != nil {
			t.Fatal(err)
		}
	}
	if result == nil {
		t.Fatal("no committed object")
	}
	var got textPayload
	if err := decodeStrict(result.Payload, &got); err != nil || got != want {
		t.Fatalf("round trip: %v", err)
	}
}

type bufferCloser struct{ io.Writer }

func (bufferCloser) Close() error { return nil }

func TestHTTPRejectsWorkerSelectedAuthority(t *testing.T) {
	binding := modelBinding{target: assistant.ModelTarget{Model: "arbitrary/alias"}, catalog: catalogPayload{}}
	valid := map[string]any{"model": "arbitrary/alias", "messages": []any{map[string]any{"role": "user", "content": "hello"}}, "stream": true}
	if err := validateModelBody(wireJSON(t, valid), binding); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"model": "different-model", "web_search_options": map[string]any{}, "functions": []any{}, "parallel_tool_calls": true, "tools": []any{map[string]any{"type": "web_search"}}} {
		copy := make(map[string]any)
		for k, v := range valid {
			copy[k] = v
		}
		copy[key] = value
		if err := validateModelBody(wireJSON(t, copy), binding); !errors.Is(err, errProtocol) {
			t.Fatalf("accepted %s=%v: %v", key, value, err)
		}
	}
	if got := completionURL("https://example.invalid/private/api/"); got != "https://example.invalid/private/api/chat/completions" {
		t.Fatalf("custom prefix = %q", got)
	}
}

func TestStreamCreditsAreBoundedAndCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &httpStream{outstanding: streamWindow, credit: make(chan struct{}, 1), cancel: cancel}
	result := make(chan error, 1)
	go func() { _, err := s.capacity(ctx); result <- err }()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	o := &operation{streams: map[string]*httpStream{"p:1": s}}
	if err := o.ackHTTP(envelope{Payload: wireJSON(t, modelAck{"p:1", streamWindow + 1})}); !errors.Is(err, errProtocol) {
		t.Fatalf("over-ack: %v", err)
	}
	if err := o.ackHTTP(envelope{Payload: wireJSON(t, modelAck{"p:1", chunkBytes})}); err != nil {
		t.Fatal(err)
	}
	available, err := s.capacity(context.Background())
	if err != nil || available != chunkBytes {
		t.Fatalf("credit = %d %v", available, err)
	}
}

func TestProtocolReaderRejectsCrossOperationEpochAndSequence(t *testing.T) {
	for _, change := range []func(*envelope){func(e *envelope) { e.OperationID = "other" }, func(e *envelope) { e.Epoch = "old" }, func(e *envelope) { e.Seq = 2 }} {
		id := "g:1"
		e := envelope{BridgeVersion, "hello", "op", "epoch", 1, &id, wireJSON(t, helloPayload{strings.Repeat("a", 64), 1})}
		change(&e)
		var wire bytes.Buffer
		if err := writeEnvelope(&wire, e); err != nil {
			t.Fatal(err)
		}
		lost := make(chan error, 1)
		p := &process{operationID: "op", epoch: "epoch", output: io.NopCloser(&wire), pending: map[string]string{"g:1": "hello"}, incoming: make(map[string]bool), hello: make(chan error, 1), readDone: make(chan struct{}), onFrame: func(envelope) error { t.Error("invalid binding dispatched"); return nil }, onLoss: func(err error) { lost <- err }}
		go p.readLoop()
		<-p.readDone
		if err := <-lost; !errors.Is(err, errProtocol) {
			t.Fatalf("binding failure: %v", err)
		}
	}
}

func TestOversizedRawArgumentsNeverReachHost(t *testing.T) {
	o := &operation{startID: "g:2"}
	p := invokePayload{OwnerRequestID: "g:2", Response: 1, CallID: "call", Name: "files_read", RawArgs: strings.Repeat("x", maxArguments+1), Batch: []proposal{{"call", "files_read", "{}"}}}
	if err := o.invoke(context.Background(), nil, envelope{Payload: wireJSON(t, p)}, false); !errors.Is(err, errProtocol) {
		t.Fatalf("raw bound: %v", err)
	}
}

func TestStrictNestedPayloadAndParkedIngress(t *testing.T) {
	var invoke invokePayload
	raw := []byte(`{"ownerRequestId":"g:2","response":1,"callId":"call","name":"files_read","rawArgs":"{}","batch":[{"callId":"call","name":"files_read"}]}`)
	if err := decodeStrict(raw, &invoke); !errors.Is(err, errProtocol) {
		t.Fatalf("missing nested rawArgs: %v", err)
	}
	for _, state := range []*operation{{pending: &envelope{}}, {activeInvoke: "p:1"}} {
		if err := state.accept(envelope{Kind: "event", Payload: wireJSON(t, eventPayload{"g:2", "answer", "out of order"})}); !errors.Is(err, errProtocol) {
			t.Fatalf("parked traffic: %v", err)
		}
	}
}

func TestFramesRejectAmbiguousJSON(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate envelope key":    `{"version":2,"version":1,"kind":"hello","operationId":"op","epoch":"epoch","seq":1,"requestId":"g:1","payload":{"manifestDigest":"a","bridgeVersion":1}}`,
		"case variant envelope key": `{"version":1,"Version":1,"kind":"hello","operationId":"op","epoch":"epoch","seq":1,"requestId":"g:1","payload":{"manifestDigest":"a","bridgeVersion":1}}`,
		"duplicate payload key":     `{"version":1,"kind":"hello","operationId":"op","epoch":"epoch","seq":1,"requestId":"g:1","payload":{"manifestDigest":"wrong","manifestDigest":"a","bridgeVersion":1}}`,
		"case variant payload key":  `{"version":1,"kind":"hello","operationId":"op","epoch":"epoch","seq":1,"requestId":"g:1","payload":{"manifestDigest":"a","bridgeVersion":1,"BridgeVersion":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var wire bytes.Buffer
			var size [4]byte
			// #nosec G115 -- raw is one of the fixed, sub-1-KiB JSON fixtures above.
			binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
			wire.Write(size[:])
			wire.WriteString(raw)
			e, err := readEnvelope(&wire)
			if err == nil {
				var hello helloPayload
				err = decodeStrict(e.Payload, &hello)
			}
			if !errors.Is(err, errProtocol) {
				t.Fatalf("ambiguous wire message accepted: %v", err)
			}
		})
	}
}

func TestHTTPRejectsAmbiguousModelAndToolDeclarations(t *testing.T) {
	binding := modelBinding{
		target: assistant.ModelTarget{Model: "approved"},
		catalog: catalogPayload{
			Tools:        []toolDefinition{{Name: "files_read", Description: "read", Parameters: json.RawMessage(`{"type":"object"}`)}},
			VisibleTools: []string{"files_read"},
		},
	}
	for name, raw := range map[string]string{
		"duplicate model":         `{"model":"other","model":"approved","messages":[{"role":"user","content":"hello"}],"stream":true,"tools":[{"type":"function","function":{"name":"files_read","description":"read","parameters":{"type":"object"}}}]}`,
		"escaped duplicate model": `{"model":"other","m\u006fdel":"approved","messages":[{"role":"user","content":"hello"}],"stream":true,"tools":[{"type":"function","function":{"name":"files_read","description":"read","parameters":{"type":"object"}}}]}`,
		"duplicate tool name":     `{"model":"approved","messages":[{"role":"user","content":"hello"}],"stream":true,"tools":[{"type":"function","function":{"name":"withheld","name":"files_read","description":"read","parameters":{"type":"object"}}}]}`,
		"case variant tool name":  `{"model":"approved","messages":[{"role":"user","content":"hello"}],"stream":true,"tools":[{"type":"function","function":{"name":"withheld","Name":"files_read","description":"read","parameters":{"type":"object"}}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateModelBody([]byte(raw), binding); !errors.Is(err, errProtocol) {
				t.Fatalf("ambiguous provider authority accepted: %v", err)
			}
		})
	}
}

func TestModelRequestTransfersFullDecodedBodyLimit(t *testing.T) {
	const prefix = `{"model":"approved","messages":[{"role":"user","content":"`
	const suffix = `"}],"stream":true}`
	body := []byte(prefix + strings.Repeat("x", maxObject-len(prefix)-len(suffix)) + suffix)
	request := modelOpen{OwnerRequestID: "g:1", Method: "POST", URL: "https://nocx.invalid/v1/chat/completions", Headers: []wireHeader{}, Body: body}
	id := "p:1"
	var wire bytes.Buffer
	p := &process{input: bufferCloser{&wire}, operationID: "op", epoch: "epoch"}
	if err := p.send("model.open", &id, request); err != nil {
		t.Fatalf("legal 4 MiB request rejected after base64 expansion: %v", err)
	}
	var objects objectReader
	var result *envelope
	for wire.Len() != 0 {
		frame, err := readEnvelope(&wire)
		if err != nil {
			t.Fatal(err)
		}
		result, err = objects.accept(frame)
		if err != nil {
			t.Fatal(err)
		}
	}
	if result == nil || result.Kind != "model.open" {
		t.Fatal("model request was not committed")
	}
	var got modelOpen
	if err := decodeStrict(result.Payload, &got); err != nil || !bytes.Equal(got.Body, body) {
		t.Fatalf("model body changed in transit: %v", err)
	}
	if err := validateModelBody(got.Body, modelBinding{target: assistant.ModelTarget{Model: "approved"}}); err != nil {
		t.Fatalf("legal reconstructed model body rejected: %v", err)
	}
	request.Body = append(body, ' ')
	o := &operation{}
	if err := o.openHTTP(envelope{RequestID: &id, Payload: wireJSON(t, request)}); !errors.Is(err, errProtocol) {
		t.Fatalf("decoded body above 4 MiB accepted: %v", err)
	}
}
