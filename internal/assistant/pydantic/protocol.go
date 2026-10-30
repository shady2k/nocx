// Package pydantic runs the experimental SDK behind private, bounded pipes.
// The Go host retains all credentials, tool authority and approval state.
package pydantic

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/jsoncheck"
)

const (
	BridgeVersion = 1
	maxFrame      = 1 << 20
	maxArguments  = 64 << 10
	chunkBytes    = 32 << 10
	maxObject     = 4 << 20
	// model.open carries a base64 body plus bounded request metadata. Its
	// decoded HTTP body still has maxObject's 4 MiB limit.
	maxModelRequestObject = (maxObject+2)/3*4 + (64 << 10)
	streamWindow          = 256 << 10
)

func objectLimit(kind string) int {
	if kind == "model.open" {
		return maxModelRequestObject
	}
	return maxObject
}

var errProtocol = errors.New("pydantic: invalid runtime protocol")

type envelope struct {
	Version     int             `json:"version"`
	Kind        string          `json:"kind"`
	OperationID string          `json:"operationId"`
	Epoch       string          `json:"epoch"`
	Seq         uint64          `json:"seq"`
	RequestID   *string         `json:"requestId"`
	Payload     json.RawMessage `json:"payload"`
}

type helloPayload struct {
	ManifestDigest string `json:"manifestDigest"`
	BridgeVersion  int    `json:"bridgeVersion"`
}
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type toolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type startPayload struct {
	Model        string           `json:"model"`
	Messages     []message        `json:"messages"`
	Tools        []toolDefinition `json:"tools"`
	VisibleTools []string         `json:"visibleTools"`
}
type generatePayload struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}
type textPayload struct {
	Text string `json:"text"`
}
type catalogRequest struct {
	OwnerRequestID string `json:"ownerRequestId"`
}
type catalogPayload struct {
	Tools        []toolDefinition `json:"tools"`
	VisibleTools []string         `json:"visibleTools"`
}
type proposal struct {
	CallID  string `json:"callId"`
	Name    string `json:"name"`
	RawArgs string `json:"rawArgs"`
}
type invokePayload struct {
	OwnerRequestID string     `json:"ownerRequestId"`
	Response       uint64     `json:"response"`
	CallID         string     `json:"callId"`
	Name           string     `json:"name"`
	RawArgs        string     `json:"rawArgs"`
	Batch          []proposal `json:"batch"`
}
type toolResult struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
}
type wireHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type modelOpen struct {
	OwnerRequestID string       `json:"ownerRequestId"`
	Method         string       `json:"method"`
	URL            string       `json:"url"`
	Headers        []wireHeader `json:"headers"`
	Body           []byte       `json:"body"`
}
type modelHeaders struct {
	Status  int          `json:"status"`
	Headers []wireHeader `json:"headers"`
}
type modelChunk struct {
	StreamID string `json:"streamId"`
	Data     []byte `json:"data"`
	Offset   int64  `json:"offset"`
}
type modelAck struct {
	StreamID string `json:"streamId"`
	AckBytes int    `json:"ackBytes"`
}
type modelEnd struct {
	StreamID string  `json:"streamId"`
	Error    *string `json:"error"`
}
type modelClose struct {
	StreamID string `json:"streamId"`
}
type eventPayload struct {
	OwnerRequestID string `json:"ownerRequestId"`
	Kind           string `json:"kind"`
	Text           string `json:"text"`
}
type errorPayload struct {
	Code   string `json:"code"`
	Status *int   `json:"status"`
}
type cancelPayload struct {
	OwnerRequestID *string `json:"ownerRequestId"`
}
type objectChunk struct {
	ObjectID   string  `json:"objectId"`
	Kind       string  `json:"kind"`
	RequestID  *string `json:"requestId"`
	TotalBytes int     `json:"totalBytes"`
	Ordinal    int     `json:"ordinal"`
	Data       []byte  `json:"data"`
}
type objectCommit struct {
	ObjectID   string  `json:"objectId"`
	Kind       string  `json:"kind"`
	RequestID  *string `json:"requestId"`
	TotalBytes int     `json:"totalBytes"`
	Chunks     int     `json:"chunks"`
	SHA256     string  `json:"sha256"`
}

func decodeStrict(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return errProtocol
	}
	if err := jsoncheck.UniqueKeys(data); err != nil {
		return fmt.Errorf("%w: %v", errProtocol, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: %v", errProtocol, err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errProtocol
	}
	return requiredFields(data, reflect.TypeOf(dst).Elem())
}

func requiredFields(data []byte, t reflect.Type) error {
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Struct {
		var elements []json.RawMessage
		if err := json.Unmarshal(data, &elements); err != nil || elements == nil {
			return errProtocol
		}
		for _, element := range elements {
			if err := requiredFields(element, t.Elem()); err != nil {
				return err
			}
		}
		return nil
	}
	// Wire fields are required, including explicit nullable fields. The native
	// provider's optional declaration fields use omitempty; names remain exact.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return errProtocol
	}
	for n := range t.NumField() {
		field := t.Field(n)
		key, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		value, ok := fields[key]
		if !ok {
			if options == "omitempty" {
				continue
			}
			return errProtocol
		}
		delete(fields, key)
		if bytes.Equal(value, []byte("null")) && field.Type.Kind() != reflect.Pointer {
			return errProtocol
		}
		if field.Type.Kind() == reflect.Struct || field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Struct {
			if err := requiredFields(value, field.Type); err != nil {
				return err
			}
		}
	}
	if len(fields) != 0 {
		return errProtocol
	}
	return nil
}

func readEnvelope(r io.Reader) (envelope, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return envelope{}, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > maxFrame {
		return envelope{}, errProtocol
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(r, data); err != nil {
		return envelope{}, err
	}
	var e envelope
	if err := decodeStrict(data, &e); err != nil {
		return envelope{}, err
	}
	if e.Version != BridgeVersion || e.Seq == 0 || e.OperationID == "" || e.Epoch == "" || !knownKind(e.Kind) {
		return envelope{}, errProtocol
	}
	return e, nil
}

func writeEnvelope(w io.Writer, e envelope) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(data) > maxFrame {
		return errProtocol
	}
	var size [4]byte
	// #nosec G115 -- len(data) is nonnegative and bounded above by maxFrame (1 MiB).
	binary.BigEndian.PutUint32(size[:], uint32(len(data)))
	if err := writeAll(w, size[:]); err != nil {
		return err
	}
	return writeAll(w, data)
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func knownKind(k string) bool {
	switch k {
	case "hello", "start", "catalog.request", "catalog.result", "tool.invoke", "tool.result", "model.open", "model.headers", "model.chunk", "model.end", "model.close", "generate", "generated", "event", "complete", "cancel", "error", "object.chunk", "object.commit":
		return true
	}
	return false
}

func requestNumber(id, prefix string) (uint64, bool) {
	if !strings.HasPrefix(id, prefix) {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(id, prefix), 10, 64)
	return n, err == nil && n > 0 && id == prefix+strconv.FormatUint(n, 10)
}

func sameID(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

type objectReader struct {
	current *objectChunk
	data    []byte
	next    int
	seen    map[string]bool
}

func (o *objectReader) accept(e envelope) (*envelope, error) {
	if e.Kind != "object.chunk" && e.Kind != "object.commit" {
		return &e, nil
	}
	if e.RequestID != nil {
		return nil, errProtocol
	}
	if e.Kind == "object.chunk" {
		var c objectChunk
		if err := decodeStrict(e.Payload, &c); err != nil {
			return nil, err
		}
		if c.ObjectID == "" || c.TotalBytes <= 0 || c.TotalBytes > objectLimit(c.Kind) || len(c.Data) == 0 || len(c.Data) > chunkBytes || !objectKind(c.Kind) {
			return nil, errProtocol
		}
		if o.current == nil {
			if c.Ordinal != 0 || o.seen[c.ObjectID] {
				return nil, errProtocol
			}
			if o.seen == nil {
				o.seen = make(map[string]bool)
			}
			o.seen[c.ObjectID] = true
			o.current = &c
			o.data = make([]byte, 0, c.TotalBytes)
			o.next = 0
		}
		p := o.current
		if c.ObjectID != p.ObjectID || c.Kind != p.Kind || !sameID(c.RequestID, p.RequestID) || c.TotalBytes != p.TotalBytes || c.Ordinal != o.next || len(o.data)+len(c.Data) > c.TotalBytes {
			return nil, errProtocol
		}
		o.data = append(o.data, c.Data...)
		o.next++
		return nil, nil
	}
	var c objectCommit
	if err := decodeStrict(e.Payload, &c); err != nil {
		return nil, err
	}
	p := o.current
	if p == nil || c.ObjectID != p.ObjectID || c.Kind != p.Kind || !sameID(c.RequestID, p.RequestID) || c.TotalBytes != p.TotalBytes || c.Chunks != o.next || len(o.data) != c.TotalBytes {
		return nil, errProtocol
	}
	digest := sha256.Sum256(o.data)
	if c.SHA256 != hex.EncodeToString(digest[:]) || !utf8.Valid(o.data) || !json.Valid(o.data) {
		return nil, errProtocol
	}
	e.Kind, e.RequestID, e.Payload = c.Kind, c.RequestID, o.data
	o.current, o.data, o.next = nil, nil, 0
	return &e, nil
}

func objectKind(k string) bool {
	return knownKind(k) && k != "object.chunk" && k != "object.commit" && k != "model.chunk" && k != "model.end" && k != "model.close"
}
