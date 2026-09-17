package host

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// Service is one named surface. Registering a second one is the whole
// extension point (D2); no service may expose an operation taking argv (D3).
type Service interface {
	Name() string
	Ops() []string
	// ParamsSchema returns the schema of the op's declared params struct,
	// or nil when the op is unknown. Every op declares its params type;
	// the dispatcher decodes a request's params through the schema.
	ParamsSchema(op string) *Schema
	Call(ctx context.Context, op string, params json.RawMessage) (any, error)
}

// CancelPolicy is an optional capability a Service implements to declare
// operations that REFUSE cancellation (D11): a cancel naming one is
// answered with ErrCodeCancelRefused and the operation runs to completion,
// because half-applying a commit is worse than waiting for it. The git
// service declares its five mutations; reads stay cancellable.
type CancelPolicy interface {
	RefusesCancel(op string) bool
}

// DataPlane is an optional capability a Service implements to receive raw
// TypeSessionData frames. The host never interprets those bytes.
type DataPlane interface {
	SessionData(context.Context, proto.SessionFrame)
}

// LifecycleDataPlane is the optional raw lifecycle carrier. It is separate
// from DataPlane so old services continue to receive PTY data unchanged.
type LifecycleDataPlane interface {
	LifecycleData(context.Context, proto.SessionFrame)
}

// ChannelDataPlane is the optional proxied-channel carrier: the raw bytes of
// an ssh channel HELD BY the service that opened it. It is its own capability
// because the identity is its own kind (proto.ChannelID, not a session) and
// because exactly one service owns ssh connections — routing these frames by
// anything but the service that can hold the channel would deliver them to
// something with nowhere to put them.
type ChannelDataPlane interface {
	// ChannelData receives one inbound frame: bytes the coordinator wrote to
	// a channel this service opened. The service moves them; it never
	// interprets them (AD-6).
	ChannelData(context.Context, proto.ChannelFrame)
}

// ResponseObserver is the optional capability a Service implements to be told
// that the response to one of its calls is already on the wire.
//
// It exists for the one ordering a service cannot arrange for itself. A
// handler that starts a background pump is a handler whose pump can write a
// frame BEFORE the dispatcher has written the response — the response is
// written after Call returns — so a caller that registers the thing the pump
// writes about, keyed by an id it only learns FROM the response, has a window
// in which bytes it has asked for are dropped as belonging to nobody.
//
// The window cannot be closed from the caller's side and must not be closed by
// a sleep, so it is closed here: this hook runs AFTER h.respond, on the same
// goroutine, which is a happens-before edge rather than a hope. A service that
// implements it may start anything it deferred; a service that does not is
// unaffected, which is what keeps this an optional capability rather than a
// new obligation on every handler.
type ResponseObserver interface {
	// ResponseWritten is called once per SUCCESSFUL call, after its response
	// frame has been written. result is what Call returned, so a service
	// holding several deferred starts can tell which one this was.
	ResponseWritten(ctx context.Context, op string, result any)
}

// RefusalCoder is an optional capability a Service implements to give its
// errors machine-readable wire codes and structured details. When Call
// returns an error the service recognises, its code (and details) cross on
// the wire instead of internal, so the backend can reconstruct the typed
// error the transport switches on — the git service's ErrNothingToCommit,
// ErrAmendUnborn, ErrConflicted and ErrNoRemote must reach the backend as
// themselves, fields intact (D11/D12).
type RefusalCoder interface {
	// Refusal codes err, returning the wire code and the structured
	// details the backend needs to rebuild the typed error. code == ""
	// means err has no special code and stays ErrCodeInternal.
	Refusal(err error) (code string, details json.RawMessage)
}

// Field is one field of an operation's declared params struct.
type Field struct {
	Name string
	typ  reflect.Type
	tag  reflect.StructTag
}

// IsFreeFormStringList reports whether the field is a bare []string — a
// free-form argument list destined for a command line. The one legitimate
// string list, a pathspec ([]string carrying a nocx:"pathspec" tag), is not
// one (D8): pathspecs name repository files, they never reach argv.
func (f Field) IsFreeFormStringList() bool {
	if f.typ == nil || f.typ.Kind() != reflect.Slice || f.typ.Elem().Kind() != reflect.String {
		return false
	}
	return f.tag.Get("nocx") != "pathspec"
}

// Schema is one operation's declared params shape, derived by reflection
// over the params struct the service declares for the op.
type Schema struct {
	typ reflect.Type
}

// SchemaFor builds the schema for an op whose params are a struct of the
// type of params (a zero value of the struct). A nil params value — a
// service declaring no type — yields an empty schema whose Decode refuses.
func SchemaFor(params any) *Schema {
	t := reflect.TypeOf(params)
	if t == nil {
		return &Schema{}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return &Schema{typ: t}
}

// Fields lists the params struct's exported fields, in declaration order.
func (s *Schema) Fields() []Field {
	if s == nil || s.typ == nil {
		return nil
	}
	t := s.typ
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	fields := make([]Field, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported fields are not wire params
		}
		fields = append(fields, Field{Name: f.Name, typ: f.Type, tag: f.Tag})
	}
	return fields
}

// Decode validates raw against the schema and returns the typed params
// value. A payload that does not unmarshal is an error the dispatcher
// answers with ErrCodeBadParams before the op runs. A request that omits
// params entirely (the wire's omitempty half) is the zero value, exactly as
// if it had carried {}.
func (s *Schema) Decode(raw json.RawMessage) (any, error) {
	if s == nil || s.typ == nil {
		return nil, errors.New("host: op has no declared params type")
	}
	v := reflect.New(s.typ).Interface()
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, v); err != nil {
			return nil, err
		}
	}
	return reflect.ValueOf(v).Elem().Interface(), nil
}
