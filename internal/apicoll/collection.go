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
	// Variables are the request's OWN, and they are rows of name, value and
	// enabled because that is the shape Query and Headers already have —
	// the model grows by one more list of the same thing rather than by a
	// new idea, and the editor draws it with the table it already has.
	//
	// WHY A REQUEST HAS ANY. A variable could only live in an environment,
	// and that is the wrong home for half of them: `id` in `/users/{{id}}`
	// belongs to the REQUEST, because two requests legitimately want
	// different ones and an environment that carried both would be a place
	// to keep other people's values. The environment's are INHERITED —
	// a name the request answers wins and everything else falls through
	// (RequestLookup) — so nothing that resolves today resolves differently.
	//
	// ONE GRAMMAR, `{{name}}`. Postman spells a path variable `:id`, and
	// what it gets right is the SCOPE rather than the syntax; a second
	// spelling would be two owners of "a hole in the address", agreeing
	// until the day somebody wrote both. The importer rewrites `:id` into
	// this grammar and says it did (internal/apiimport).
	//
	// `omitempty` for the same reason every other list here has it: a file
	// with no variables says nothing about them. The WIRE is the opposite —
	// the renderer's first .map on a null throws, so the transport forces
	// the list non-nil.
	Variables []Param `json:"variables,omitempty"`
	Body      Body    `json:"body"`
	Auth      Auth    `json:"auth"`

	// folderVariables are inherited metadata attached only while a request
	// is read for a send. They are not part of the request file or wire
	// model: persisting or returning them would turn a folder scope into a
	// second request scope. ReadRequest attaches the nearest-first rows so
	// RequestLookup can keep the existing request → environment seam while
	// adding the folder scope between them.
	folderVariables []Param
	// folderVariableSources parallels folderVariables and keeps the folder
	// path that supplied each row for the effective-scope projection. It is
	// private for the same reason as folderVariables: source metadata is not
	// part of the request file or request.read wire shape.
	folderVariableSources []string
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

// Auth holds TEXT, like every other field in the format. The bearer token,
// the basic password and the username are the values the person typed —
// possibly `{{variable}}` references, resolved by the same substitution as
// the URL, a header or the body (design §6.5, nocx-6hg2w.20).
//
// The plain-vs-vault distinction is BY CONSTRUCTION, not by heuristic: a
// variable the binding document answers is a secret. A literal the person
// pasted stays text in the file, is SENT, and is written to their file —
// the decision recorded in nocx-tg9l8: the product does not hide or move a
// credential a person typed. Design §8 is unchanged: a file still cannot
// NAME a secret, because there is no syntax in which a file names one — a
// vault identifier typed here is simply the literal it is, and the binding
// from a name to a stored value lives in internal/apibind, nowhere in this
// folder.
type Auth struct {
	Kind string `json:"kind"`
	User string `json:"user,omitempty"`
	// Token is the bearer token, or the api-key value. Text: it may be a
	// literal the person pasted or a `{{variable}}` reference.
	Token string `json:"token,omitempty"`
	// Password is the basic-auth password, as the same text.
	Password string `json:"password,omitempty"`
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
	// Folders names every directory inside the collection, each as a path
	// relative to the root, parents before their children.
	//
	// It is here because a folder with nothing in it yet is still a folder,
	// and until this list existed there was no way to say so: the tree
	// derived its shape from the request paths, so a folder a person had
	// just made was invisible until they put something in it — which is a
	// folder that does not exist as far as they are concerned.
	//
	// It is also the ONE answer to "what folders are there". A surface
	// deriving them from the request paths as well would be a second
	// derivation of one fact, agreeing with this one for every folder that
	// holds a request and disagreeing about every folder that does not.
	//
	// `environments/` is not among them (§6.2), nor is anything beginning
	// with a dot: they are the same exclusions the request walk already
	// makes, because this list comes off the same walk.
	Folders []string `json:"folders"`
	// VariableFolders is the subset of Folders carrying the reserved
	// `.variables.json` file, plus "" when the collection root carries one.
	// It rides on the collection listing because the listing already answers
	// which folders exist; asking once per folder would create a second,
	// round-trip-separated answer to the same tree fact. Values stay on disk
	// and never cross this listing.
	VariableFolders []string `json:"variableFolders"`
	// Malformed names the files that could not be read as requests. It is
	// ON the collection, not in an error beside it, and that placement is the
	// whole point: a caller which returns early on err != nil would
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
