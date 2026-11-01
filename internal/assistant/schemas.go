package assistant

// The dispatch schemas are compiled once per process, because their inputs are
// constants of the BUILD.
//
// A tool's params and result contracts are files in internal/agenttools'
// embedded tool set, read once at Assemble; every App, every kernel and every
// test stand in one process then compiles the same documents again for itself.
// Measured on the package's own profile (nocx-atpw5.1.17): 3.25 s of the
// internal/app suite was compileDispatchSchemas, for 126 App starts compiling
// one unchanged set of contracts. The reuse is not a test-only trick — a
// process that builds two dispatchers over one registry is exactly what
// cmd/devharness and the App do — and the value it shares is immutable, which
// is what makes it safe to share.
//
// The pattern is internal/transport's (nocx-atpw5.1.6): one owner holds what an
// immutable input set compiles to, and a different input set is a different
// entry by construction rather than by a caller remembering to clear it.

import (
	"bytes"
	"crypto/sha256"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// toolSchemaKey names one compile: the resource URL the document is compiled
// under and the document's own bytes. Both are inputs of the compile, so two
// calls share a result exactly when they would have produced the same one — a
// contract that changed, or a params document compiled under a result URL,
// never reads another's entry.
type toolSchemaKey struct {
	url  string
	hash [sha256.Size]byte
}

// toolSchemas holds each contract's compiled schema for the life of the
// process. The entries are bounded by the set of contracts the build carries,
// not by how many Apps, dispatchers or stands ask for them.
type toolSchemas struct {
	mu sync.Mutex
	// byKey is the compiled document. A *jsonschema.Schema is immutable once
	// Compile returns: every caller validates with it and none mutates it, so
	// one value is safe for concurrent use, which is what lets this be shared
	// without a lock around validation.
	byKey map[toolSchemaKey]*jsonschema.Schema
}

var compiledToolSchemas = &toolSchemas{byKey: map[toolSchemaKey]*jsonschema.Schema{}}

// compiledSchema answers the compiled form of one contract document, compiling
// it the first time this process asks for it.
//
// A FAILED compile is not remembered: the same broken contract is reported
// every time rather than answered from a cache, because an error is not a
// schema and a caller that fixes the document must be able to see the fix.
//
// The lock is held across the compile, which is what makes one compile per
// document true rather than likely when two dispatchers are built at once; the
// critical section is a first-start cost measured in microseconds per
// contract, and a different document waits for it rather than repeating it.
func compiledSchema(url string, raw []byte) (*jsonschema.Schema, error) {
	key := toolSchemaKey{url: url, hash: sha256.Sum256(raw)}
	compiledToolSchemas.mu.Lock()
	defer compiledToolSchemas.mu.Unlock()
	if s, ok := compiledToolSchemas.byKey[key]; ok {
		return s, nil
	}
	s, err := compileSchemaDocument(url, raw)
	if err != nil {
		return nil, err
	}
	compiledToolSchemas.byKey[key] = s
	return s, nil
}

// compileSchemaDocument compiles one document under one resource URL. It is
// the whole of what a compile is; the callers own the error's wording.
func compileSchemaDocument(url string, raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if addErr := c.AddResource(url, doc); addErr != nil {
		return nil, addErr
	}
	return c.Compile(url)
}
