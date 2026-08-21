// Package apicoll owns the API-testing collection: the model, its on-disk
// JSON format and the folder that holds it.
//
// The one rule that shapes every type here: A COLLECTION FILE NAMES A
// VARIABLE, NEVER A SECRET (design §8). There is deliberately no field in
// which a file can spell an identifier for stored credential material, so a
// folder arriving in a pull request has no way to reach the password behind
// an SSH profile. The binding from a variable name to a stored value lives
// in internal/apibind, which is the only thing that holds such an
// identifier.
//
// This file is the type skeleton, landed by the coordinator so the importers
// and the sender could be written against it in parallel. Behaviour belongs
// beside it, not in it.
package apicoll

// Request is the model. Both projections — the form now, the HTTPie-style
// line later — are views of this, and the file is the truth rather than
// either of them (design §6.4).
type Request struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Method  string   `json:"method"`
	URL     string   `json:"url"`
	Headers []Header `json:"headers,omitempty"`
	Query   []Param  `json:"query,omitempty"`
	Body    Body     `json:"body"`
	Auth    Auth     `json:"auth"`
}

// Header and Param carry Enabled because a disabled row is a row the user
// keeps: deleting it to turn it off loses the value they will want back.
type Header struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

type Param struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// BodyKind values. A body too large or too awkward for a line is named by a
// file rather than lost — HTTPie's own answer to its own limit (§6.4).
//
// JSON is its own kind rather than raw-with-a-header, and the difference is
// what the kind DECLARES. Raw declares nothing: the user's own Content-Type
// header is the only answer, and guessing one would send a header they did
// not write (apisend.requestBody). JSON is the user saying which format this
// is, so the header follows from the kind the way the form kind's already
// does — and the editor above it can highlight what it now knows it is.
const (
	BodyNone = "none"
	BodyRaw  = "raw"
	BodyJSON = "json"
	BodyForm = "form"
	BodyFile = "file"
)

type Body struct {
	Kind    string `json:"kind"`
	Text    string `json:"text,omitempty"`
	FileRef string `json:"fileRef,omitempty"`
}

// AuthKind values.
const (
	AuthNone   = "none"
	AuthBearer = "bearer"
	AuthBasic  = "basic"
	AuthAPIKey = "apikey"
)

// Auth names a VARIABLE, never a secret. Var is looked up as a variable name
// in the current environment; a file that puts a vault identifier here has
// merely named a variable nobody bound, and the send is blocked as
// unresolved. That is the whole of §8: the attack is unspellable rather than
// guarded.
type Auth struct {
	Kind string `json:"kind"`
	Var  string `json:"var,omitempty"`
	User string `json:"user,omitempty"`
}

// RouteKind values. The route lives on the ENVIRONMENT, never on a request
// (§6.5): baseUrl answers "where" and the route answers "how to get there",
// and one record cannot drift from itself.
const (
	RouteDirect     = "direct"
	RouteConnection = "connection"
)

type Route struct {
	Kind      string `json:"kind"`
	ProfileID string `json:"profileId,omitempty"`
	// InsecureTLS sends without verifying the server's certificate.
	//
	// It is on the ROUTE because it is part of how a destination is reached,
	// the same question the kind and the profile answer — a development host
	// with a self-signed certificate is reached that way or not at all.
	//
	// It is per ENVIRONMENT and not per app, and that is deliberate: a
	// person turns it on for the dev environment and cannot thereby turn it
	// on for production, which is what a global switch would do the moment
	// they forgot it was set. It is in the FILE, so a colleague reviewing
	// the collection sees it in the diff; and every run that used it says so
	// in the panel, so it can never be quietly on.
	InsecureTLS bool `json:"insecureTls,omitempty"`
}

// Environment holds plain values, the NAMES of its secret variables, and the
// route. It holds no secret values and no identifiers for them.
type Environment struct {
	Name       string            `json:"name"`
	Values     map[string]string `json:"values,omitempty"`
	SecretVars []string          `json:"secretVars,omitempty"`
	Route      Route             `json:"route"`
}

// Collection is a folder. Requests are addressed by their path within it,
// relative to a backend-held handle — never by a path the renderer supplies
// twice (§13.1).
type Collection struct {
	Name     string       `json:"name"`
	Requests []RequestRef `json:"requests"`
	// Malformed names the files that could not be read as requests. It is
	// ON the collection, not in an error beside it, and that placement is
	// the whole point: a caller which returns early on err != nil would
	// otherwise make one broken file hide every good one. One bad file is a
	// bad file, never a collection that will not open.
	Malformed []MalformedRef `json:"malformed,omitempty"`
}

// MalformedRef is a file inside the collection that is not a request — bad
// JSON, a field the format does not declare, a symlink that was not followed.
// Reason is for a person: it says which file and what was wrong with it.
type MalformedRef struct {
	RelPath string `json:"relPath"`
	Reason  string `json:"reason"`
}

type RequestRef struct {
	RelPath string `json:"relPath"`
	Name    string `json:"name"`
	Method  string `json:"method"`
}

// HandleID is minted by the backend when a collection is opened. Every later
// call names this plus a path relative to it; `root` is accepted once and
// never again.
type HandleID string
