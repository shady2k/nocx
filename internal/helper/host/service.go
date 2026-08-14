package host

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
