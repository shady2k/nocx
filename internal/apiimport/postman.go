package apiimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/pathname"
)

// MaxDocumentBytes bounds the import document. A Postman export with saved
// responses is comfortably inside this; anything past it is not an export
// somebody made, and an unbounded read makes the reader's memory the
// caller's to choose.
//
// Exported because the URL entrance bounds its read by the same number, and
// two spellings of one ceiling is two ceilings.
const MaxDocumentBytes = 16 << 20 // 16 MiB

// defaultEnvName is the environment a collection export's own variables
// become. A Postman collection's variables are not scoped to an
// environment, and ours must be (§6.5), so they land in one named
// environment rather than in each of them.
const defaultEnvName = "default"

// postmanResult is a converted document: the collection, its requests read
// two ways — Collection.Requests[i].RelPath is where Requests[i] belongs,
// because a request is addressed by its path within the collection (§6.1)
// and the model itself holds no path — its environments, the secret VALUES,
// and what could not be carried.
//
// It is unexported, and the values are why. There was a public FromPostman
// returning everything but them; nothing outside this package's own tests
// ever called it, because api.import.postman WRITES A FOLDER rather than
// answering with a collection, so it was a converter with no entrance. The
// entrance is ImportInto, which has a BindWriter to hand the values to (§8).
type postmanResult struct {
	Collection   apicoll.Collection
	Requests     []apicoll.Request
	Environments []apicoll.Environment
	Secrets      []secretOffer
	Unsupported  []Unsupported
}

// ---- the document, as much of it as we read ----

// pmString accepts anything JSON can hold in a place Postman documents a
// string. A hostile or merely old export that puts a number where a string
// belongs is a document we still import, rather than one field failing the
// whole thing.
type pmString string

func (s *pmString) UnmarshalJSON(b []byte) error {
	switch {
	case len(b) == 0 || string(b) == "null":
		*s = ""
	case b[0] == '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = pmString(v)
	default:
		*s = pmString(bytes.TrimSpace(b))
	}
	return nil
}

func (s pmString) String() string { return string(s) }

type pmDoc struct {
	Info     *pmInfo         `json:"info"`
	Item     []pmItem        `json:"item"`
	Variable []pmVariable    `json:"variable"`
	Auth     *pmAuth         `json:"auth"`
	Event    []pmEvent       `json:"event"`
	PPB      json.RawMessage `json:"protocolProfileBehavior"`

	// The environment-export shape.
	Name   pmString     `json:"name"`
	Values []pmVariable `json:"values"`
	Scope  pmString     `json:"_postman_variable_scope"`
}

type pmInfo struct {
	Name        pmString        `json:"name"`
	Schema      pmString        `json:"schema"`
	Description json.RawMessage `json:"description"`
}

type pmItem struct {
	Name        pmString          `json:"name"`
	Item        []pmItem          `json:"item"`
	Request     json.RawMessage   `json:"request"`
	Response    []json.RawMessage `json:"response"`
	Event       []pmEvent         `json:"event"`
	Auth        *pmAuth           `json:"auth"`
	PPB         json.RawMessage   `json:"protocolProfileBehavior"`
	Description json.RawMessage   `json:"description"`
}

type pmRequest struct {
	Method      pmString        `json:"method"`
	Header      json.RawMessage `json:"header"`
	URL         json.RawMessage `json:"url"`
	Body        *pmBody         `json:"body"`
	Auth        *pmAuth         `json:"auth"`
	Description json.RawMessage `json:"description"`
}

type pmHeader struct {
	Key      pmString `json:"key"`
	Value    pmString `json:"value"`
	Disabled bool     `json:"disabled"`
}

type pmURL struct {
	Raw      pmString        `json:"raw"`
	Protocol pmString        `json:"protocol"`
	Host     json.RawMessage `json:"host"`
	Path     json.RawMessage `json:"path"`
	Port     pmString        `json:"port"`
	Query    []pmQuery       `json:"query"`
	// Postman's PATH variables: `/users/:id` with the value for `id` kept
	// beside the address. Read only so the import can SAY it is dropping
	// them — see readURL. The field was absent before, which made the loss
	// silent, and the ask promises the opposite in as many words.
	Variable []pmVariable `json:"variable"`
}

type pmQuery struct {
	Key      pmString `json:"key"`
	Value    pmString `json:"value"`
	Disabled bool     `json:"disabled"`
}

type pmBody struct {
	Mode       string          `json:"mode"`
	Raw        pmString        `json:"raw"`
	Urlencoded []pmFormParam   `json:"urlencoded"`
	Formdata   []pmFormParam   `json:"formdata"`
	File       *pmFile         `json:"file"`
	GraphQL    json.RawMessage `json:"graphql"`
	Disabled   bool            `json:"disabled"`
}

type pmFormParam struct {
	Key      pmString `json:"key"`
	Value    pmString `json:"value"`
	Src      pmString `json:"src"`
	Type     string   `json:"type"`
	Disabled bool     `json:"disabled"`
}

type pmFile struct {
	Src pmString `json:"src"`
}

type pmAuth struct {
	Type   string        `json:"type"`
	Bearer []pmAuthParam `json:"bearer"`
	Basic  []pmAuthParam `json:"basic"`
	APIKey []pmAuthParam `json:"apikey"`
}

type pmAuthParam struct {
	Key   string   `json:"key"`
	Value pmString `json:"value"`
}

func (a *pmAuth) param(list []pmAuthParam, key string) string {
	for _, p := range list {
		if strings.EqualFold(p.Key, key) {
			return p.Value.String()
		}
	}
	return ""
}

type pmVariable struct {
	ID       pmString `json:"id"`
	Key      pmString `json:"key"`
	Value    pmString `json:"value"`
	Type     string   `json:"type"`
	Disabled bool     `json:"disabled"`
	Enabled  *bool    `json:"enabled"`
}

func (v pmVariable) off() bool { return v.Disabled || (v.Enabled != nil && !*v.Enabled) }

type pmEvent struct {
	Listen   pmString        `json:"listen"`
	Script   json.RawMessage `json:"script"`
	Disabled bool            `json:"disabled"`
}

// ---- conversion ----

type pmConv struct {
	res   postmanResult
	alloc *pathAllocator
	namer *varNamer
	env   *apicoll.Environment
	// route is how the DOCUMENT arrived, not where its requests go: every
	// environment this converter mints leaves by it (§6).
	route      apicoll.Route
	items      int
	descs      int
	behaviours int
}

// arrivalRoute is the route the document arrived through, normalised: an
// empty kind is `direct`, and InsecureTLS is dropped — it is the
// environment's own choice (apicoll/collection.go:126) and a one-off fetch
// may not make it for every request the collection will ever send.
func (c *pmConv) arrivalRoute() apicoll.Route {
	if c.route.Kind != apicoll.RouteConnection {
		return apicoll.Route{Kind: apicoll.RouteDirect}
	}
	return apicoll.Route{Kind: apicoll.RouteConnection, ProfileID: c.route.ProfileID}
}

func parsePostman(r io.Reader, route apicoll.Route) (postmanResult, error) {
	var res postmanResult

	raw, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return res, fmt.Errorf("apiimport: read import document: %w", err)
	}
	if len(raw) > MaxDocumentBytes {
		return res, fmt.Errorf("apiimport: import document is over the %d-byte limit", MaxDocumentBytes)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return res, errors.New("apiimport: the import document is empty")
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	var doc pmDoc
	if err := dec.Decode(&doc); err != nil {
		return res, fmt.Errorf("apiimport: parse import document: %w", err)
	}
	// Trailing data is not a document with a suffix; it is two documents,
	// and we would silently import the first.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return res, errors.New("apiimport: trailing data after the import document")
	}

	c := &pmConv{alloc: newPathAllocator(), namer: newVarNamer(), route: route}

	switch {
	case doc.Info == nil && doc.Item == nil && doc.Values != nil:
		// An environment export.
		name := strings.TrimSpace(doc.Name.String())
		if name == "" {
			name = defaultEnvName
		}
		c.env = &apicoll.Environment{Name: name, Route: c.arrivalRoute()}
		c.readVariables(doc.Values)
		c.res.Environments = append(c.res.Environments, *c.env)
		return c.res, nil

	case doc.Info != nil || doc.Item != nil:
		return c.collection(doc)

	default:
		return res, errors.New("apiimport: not a Postman collection or environment (no info, item or values)")
	}
}

func (c *pmConv) itemise(what, why string) {
	c.res.Unsupported = append(c.res.Unsupported, Unsupported{What: what, Why: why})
}

func (c *pmConv) collection(doc pmDoc) (postmanResult, error) {
	name := ""
	if doc.Info != nil {
		name = strings.TrimSpace(doc.Info.Name.String())
	}
	if name == "" {
		name = "Imported collection"
	}
	c.res.Collection.Name = name
	c.res.Collection.Requests = []apicoll.RequestRef{}

	if len(doc.Variable) > 0 {
		c.env = &apicoll.Environment{Name: defaultEnvName, Route: c.arrivalRoute()}
		c.readVariables(doc.Variable)
	}
	if len(doc.Event) > 0 {
		c.itemise("scripts on the collection", "pre-request and test scripts are not carried; nothing in this product runs JavaScript from a collection")
	}
	if len(doc.PPB) > 0 {
		c.behaviours++
	}
	if doc.Info != nil && len(doc.Info.Description) > 0 {
		c.descs++
	}

	if err := c.walk(doc.Item, "", 1, doc.Auth); err != nil {
		return c.res, err
	}

	if c.descs > 0 {
		c.itemise(fmt.Sprintf("descriptions (%d)", c.descs), "the model has no description field; the text is documentation rather than behaviour and was not carried")
	}
	if c.behaviours > 0 {
		c.itemise(fmt.Sprintf("protocolProfileBehavior (%d)", c.behaviours),
			"per-request protocol switches (redirect handling, encoding, body strictness) are the send's HTTP policy here, not the request (design §7.3)")
	}
	if c.env != nil {
		c.res.Environments = append(c.res.Environments, *c.env)
	}
	return c.res, nil
}

// readVariables splits a Postman variable list into what the environment
// file may hold and what it may not.
//
// The rule is §6.3 exactly: a "secret" variable contributes its NAME and
// nothing else. A variable NOT marked secret whose value is
// credential-shaped is promoted to the same treatment and said out loud —
// leaving a live token in a file that exists to be committed is the failure
// this whole format is built to make impossible, and Postman users mark
// perhaps half of theirs.
func (c *pmConv) readVariables(vars []pmVariable) {
	for _, v := range vars {
		name := strings.TrimSpace(v.Key.String())
		if name == "" {
			c.itemise("a variable with no name", "a variable is addressed by name and this one has none")
			continue
		}
		c.namer.reserve(name)
		if v.off() {
			c.itemise("disabled variable "+clip(name), "the model has no disabled state for a variable; an environment holds the ones in use")
			continue
		}
		switch {
		case strings.EqualFold(v.Type, "secret"):
			c.declareSecret(name, v.Value.String())
		case headerValueIsSecret(name, v.Value.String()):
			c.declareSecret(name, v.Value.String())
			c.itemise("variable "+clip(name)+" was not marked secret",
				"its value is credential-shaped, so it was stored as a secret variable: the name is in the environment file and the value is not")
		default:
			c.ensureEnv()
			if c.env.Values == nil {
				c.env.Values = map[string]string{}
			}
			c.env.Values[name] = v.Value.String()
		}
	}
}

func (c *pmConv) ensureEnv() {
	if c.env == nil {
		c.env = &apicoll.Environment{Name: defaultEnvName, Route: c.arrivalRoute()}
	}
}

// declareSecret records the NAME in the environment and hands the VALUE to
// the offer list, which only ImportInto reads.
func (c *pmConv) declareSecret(name, value string) {
	c.ensureEnv()
	for _, existing := range c.env.SecretVars {
		if existing == name {
			return
		}
	}
	c.env.SecretVars = append(c.env.SecretVars, name)
	c.res.Secrets = append(c.res.Secrets, secretOffer{Environment: c.env.Name, Variable: name, Value: []byte(value)})
}

func (c *pmConv) walk(items []pmItem, dir string, depth int, inherited *pmAuth) error {
	// The bound is the PATH's, not the folder chain's, and it is stated once
	// (pathname.MaxDepth) rather than here as well: what the store and the
	// filesystem have to take is the finished path, and a request inside
	// this chain is one component deeper than the chain. depth counts from
	// 1, so at depth d the folder chain is d-1 long and a request in it is a
	// d-component path — which is why the comparison is against the path
	// bound itself and the sentence names one less.
	if depth > pathname.MaxDepth {
		return fmt.Errorf("apiimport: folders nested deeper than %d", pathname.MaxDepth-1)
	}
	for _, it := range items {
		c.items++
		if c.items > maxItems {
			return fmt.Errorf("apiimport: more than %d items", maxItems)
		}
		auth := inherited
		if it.Auth != nil {
			auth = it.Auth
		}
		if len(it.Event) > 0 {
			c.itemise("scripts on "+clip(it.Name.String()), "pre-request and test scripts are not carried; nothing in this product runs JavaScript from a collection")
		}
		if len(it.Response) > 0 {
			c.itemise(fmt.Sprintf("saved responses on %s (%d)", clip(it.Name.String()), len(it.Response)),
				"a saved example is a record of a past run; the run list holds those here, and an import fires no request to produce one")
		}
		if len(it.PPB) > 0 {
			c.behaviours++
		}
		if len(it.Description) > 0 {
			c.descs++
		}

		switch {
		case it.Item != nil:
			sub := dir
			if len(it.Item) > 0 {
				sub = c.alloc.take(dir, it.Name.String(), fallbackFolder, "")
			}
			if err := c.walk(it.Item, sub, depth+1, auth); err != nil {
				return err
			}
		case len(it.Request) > 0:
			c.request(it, dir, auth)
		default:
			c.itemise("item "+clip(it.Name.String()), "it is neither a request nor a folder")
		}
	}
	return nil
}

func (c *pmConv) request(it pmItem, dir string, inherited *pmAuth) {
	var pr pmRequest
	// The short form: "request" is the URL and nothing else.
	var asURL string
	if err := json.Unmarshal(it.Request, &asURL); err == nil {
		pr.URL = it.Request
	} else if err := json.Unmarshal(it.Request, &pr); err != nil {
		c.itemise("request "+clip(it.Name.String()), "its request object could not be read: "+err.Error())
		return
	}
	if len(pr.Description) > 0 {
		c.descs++
	}

	relPath := c.alloc.take(dir, it.Name.String(), fallbackRequest, ".json")
	req := apicoll.Request{
		ID:     requestID(relPath),
		Name:   it.Name.String(),
		Method: strings.ToUpper(strings.TrimSpace(pr.Method.String())),
		Auth:   apicoll.Auth{Kind: apicoll.AuthNone},
	}
	if req.Method == "" {
		req.Method = "GET"
	}

	base, query, pathVars := c.readURL(pr.URL, clip(it.Name.String()))
	req.URL = base
	req.Query = query
	req.Variables = pathVars

	headers := c.readHeaders(pr.Header, clip(it.Name.String()))

	if pr.Body != nil {
		req.Body = c.readBody(pr.Body, clip(it.Name.String()))
	} else {
		req.Body = apicoll.Body{Kind: apicoll.BodyNone}
	}

	// Auth: the request's own, else the nearest folder's, else the
	// collection's. Postman's default is to inherit, so NOT resolving it
	// would silently unauthenticate every request in a collection that
	// declares its auth once at the top.
	auth := pr.Auth
	if auth == nil || auth.Type == "" || strings.EqualFold(auth.Type, "inherit") {
		auth = inherited
	}
	headers = c.applyAuth(&req, auth, headers, clip(it.Name.String()))

	kept, headerAuth, offers, unsup := absorbHeaderSecrets(headers, c.namer)
	req.Headers = kept
	c.res.Unsupported = append(c.res.Unsupported, unsup...)
	for _, o := range offers {
		c.offerSecret(o)
	}
	if headerAuth != nil {
		req.Auth = *headerAuth
	}

	c.res.Requests = append(c.res.Requests, req)
	c.res.Collection.Requests = append(c.res.Collection.Requests, apicoll.RequestRef{
		RelPath: relPath,
		Name:    req.Name,
		Method:  req.Method,
	})
}

// offerSecret routes a value found inside a request to the environment that
// declares its name, creating that environment if the collection had none:
// a binding whose variable no file declares is a binding nothing resolves.
func (c *pmConv) offerSecret(o secretOffer) {
	c.ensureEnv()
	o.Environment = c.env.Name
	for _, existing := range c.env.SecretVars {
		if existing == o.Variable {
			c.res.Secrets = append(c.res.Secrets, o)
			return
		}
	}
	c.env.SecretVars = append(c.env.SecretVars, o.Variable)
	c.res.Secrets = append(c.res.Secrets, o)
}

// applyAuth maps Postman's auth onto the model's, returning the headers
// with any auth-carrying header added.
//
// apikey has a header or parameter NAME, and apicoll.Auth has nowhere to
// hold one. Rather than add a second field meaning what a header already
// means, an apikey becomes the header or query parameter it actually is.
func (c *pmConv) applyAuth(req *apicoll.Request, a *pmAuth, headers []apicoll.Header, item string) []apicoll.Header {
	if a == nil {
		return headers
	}
	switch strings.ToLower(a.Type) {
	case "", "inherit", "noauth":
		return headers

	case "bearer":
		token := a.param(a.Bearer, "token")
		if name, ok := varRef(token); ok {
			c.namer.reserve(name)
			req.Auth = apicoll.Auth{Kind: apicoll.AuthBearer, Token: "{{" + name + "}}"}
			return headers
		}
		v := c.namer.take("token")
		req.Auth = apicoll.Auth{Kind: apicoll.AuthBearer, Token: "{{" + v + "}}"}
		c.offerSecret(secretOffer{Variable: v, Value: []byte(token)})
		return headers

	case "basic":
		user := a.param(a.Basic, "username")
		pass := a.param(a.Basic, "password")
		if name, ok := varRef(pass); ok {
			c.namer.reserve(name)
			req.Auth = apicoll.Auth{Kind: apicoll.AuthBasic, User: user, Password: "{{" + name + "}}"}
			return headers
		}
		v := c.namer.take("password")
		req.Auth = apicoll.Auth{Kind: apicoll.AuthBasic, User: user, Password: "{{" + v + "}}"}
		c.offerSecret(secretOffer{Variable: v, Value: []byte(pass)})
		return headers

	case "apikey":
		key := a.param(a.APIKey, "key")
		value := a.param(a.APIKey, "value")
		in := strings.ToLower(a.param(a.APIKey, "in"))
		if key == "" {
			c.itemise("apikey auth on "+item, "it names no header or parameter to carry the key")
			return headers
		}
		if in == "query" {
			req.Query = append(req.Query, apicoll.Param{Name: key, Value: value, Enabled: true})
			return headers
		}
		return append(headers, apicoll.Header{Name: key, Value: value, Enabled: true})

	default:
		// The credential inside an auth type we cannot map is dropped here
		// and reaches no file: there is no field in which one may be
		// spelled (§8), so "carry it just in case" is not available.
		c.itemise(a.Type+" auth on "+item,
			"the model holds bearer, basic and api-key auth; the request was imported unauthenticated and no credential was written")
		return headers
	}
}

func (c *pmConv) readHeaders(raw json.RawMessage, item string) []apicoll.Header {
	if len(raw) == 0 {
		return nil
	}
	var list []pmHeader
	if err := json.Unmarshal(raw, &list); err == nil {
		out := make([]apicoll.Header, 0, len(list))
		for _, h := range list {
			name := strings.TrimSpace(h.Key.String())
			if name == "" {
				continue
			}
			out = append(out, apicoll.Header{Name: name, Value: h.Value.String(), Enabled: !h.Disabled})
		}
		return out
	}
	// Postman also allows the whole header block as one string.
	var block string
	if err := json.Unmarshal(raw, &block); err == nil {
		var out []apicoll.Header
		for _, line := range strings.Split(block, "\n") {
			name, value, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(name) == "" {
				continue
			}
			out = append(out, apicoll.Header{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value), Enabled: true})
		}
		return out
	}
	c.itemise("headers on "+item, "the header list could not be read in either of the two shapes Postman writes")
	return nil
}

// readURL returns the URL with its query taken off, and the query.
//
// Postman's STRUCTURED query is taken verbatim and a query split out of a
// raw URL is percent-decoded — the same rule splitQuery states for curl,
// and the reason there is one rule is that two would disagree on exactly
// one input.
func (c *pmConv) readURL(raw json.RawMessage, item string) (string, []apicoll.Param, []apicoll.Param) {
	if len(raw) == 0 {
		return "", nil, nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		// A URL Postman wrote as a bare string carries no `variable` list
		// beside it, but it can still spell a `:name` — so the rewrite runs
		// here too and the panel gets a row to fill.
		plain, query := splitQuery(asString)
		rewritten, vars := c.readPathVariables(plain, nil, item)
		return rewritten, query, vars
	}
	var u pmURL
	if err := json.Unmarshal(raw, &u); err != nil {
		c.itemise("url on "+item, "the url could not be read in either of the two shapes Postman writes")
		return "", nil, nil
	}

	base := u.Raw.String()
	if base == "" {
		base = assembleURL(u)
	}
	base, decoded := splitQuery(base)
	base, pathVars := c.readPathVariables(base, u.Variable, item)
	if len(u.Query) == 0 {
		return base, decoded, pathVars
	}
	out := make([]apicoll.Param, 0, len(u.Query))
	for _, q := range u.Query {
		name := q.Key.String()
		if name == "" {
			continue
		}
		out = append(out, apicoll.Param{Name: name, Value: q.Value.String(), Enabled: !q.Disabled})
	}
	return base, out, pathVars
}

// notePathVariables says what a `/users/:id` address loses on the way in.
//
// Postman keeps a per-REQUEST value beside the address: the path carries
// `:id` and `url.variable` carries `id = 54321`. This model has no
// per-request variable — an environment answers `{{name}}` and its scope is
// the collection — so the address arrives with `:id` still in it and the
// value has nowhere to go. That is a real loss and it was a SILENT one: the
// field was not even read, so nothing could report it, while the import ask
// promises that "what the format cannot carry is named afterwards rather
// than dropped".
//
// Both shapes are reported, because both lose something different. With
// values, the values are what is gone. Without them — an export that
// templated the path and never filled it in — what is gone is any way to
// resolve `:id` at all, and a request that goes out with a literal colon
// segment is a request to a URL nobody meant.
// readPathVariables turns Postman's path variables into ours: the address
// keeps a hole and the value moves into the request's OWN table.
//
// IT IS A REPORTED CHANGE NOW, NOT A REPORTED LOSS. Until the request had
// variables of its own there was nowhere for `id = 54321` to go, so this
// function itemised what had been dropped; the address arrived with `:id`
// still in it and nothing could resolve it. What it says now is what it did.
//
// ONE GRAMMAR. `:id` becomes `{{id}}` rather than being kept as a second
// spelling of the same hole — two spellings would be two owners of "a hole
// in the address", agreeing until the day somebody wrote both. Postman's
// scope is what we took; its syntax is not.
//
// A `:name` WITH NO VALUE is still rewritten, with an empty value beside it.
// The alternative is an address that can never resolve and a table with
// nothing in it to fix; this way the panel has a row to offer, and the send
// refuses by naming the variable (§6.5) instead of dialling a URL with a
// colon segment in it.
func (c *pmConv) readPathVariables(base string, vars []pmVariable, item string) (string, []apicoll.Param) {
	values := make(map[string]string, len(vars))
	declared := make([]string, 0, len(vars))
	for _, v := range vars {
		name := strings.TrimSpace(v.Key.String())
		if name == "" || v.off() {
			continue
		}
		if _, already := values[name]; !already {
			declared = append(declared, name)
		}
		values[name] = v.Value.String()
	}

	rewritten, found := rewriteColonSegments(base)
	if len(found) == 0 && len(declared) == 0 {
		return base, nil
	}

	// One row per name the ADDRESS uses, in the order it uses them, so the
	// table reads like the URL. A variable the export declared but the
	// address never mentions is not a hole to fill and is reported rather
	// than carried — inventing a row for it would put a value in a file
	// under a name nothing reads.
	out := make([]apicoll.Param, 0, len(found))
	seen := make(map[string]bool, len(found))
	for _, name := range found {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, apicoll.Param{Name: name, Value: values[name], Enabled: true})
	}

	var unused []string
	for _, name := range declared {
		if !seen[name] {
			unused = append(unused, name)
		}
	}
	if len(unused) > 0 {
		c.itemise("path variables on "+item+" ("+strings.Join(unused, ", ")+")",
			"the export declared them and the address never used them, so there was no hole to carry them into")
	}
	if len(out) > 0 {
		c.itemise("path variables on "+item+" ("+strings.Join(namesOf(out), ", ")+")",
			"Postman's `:name` segments became `{{name}}` — one grammar for a hole in an address — and the values moved into the request's own variables")
	}
	return rewritten, out
}

// namesOf lists the rows' names, for a sentence that itemises them.
func namesOf(rows []apicoll.Param) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// rewriteColonSegments replaces every `/:name` with `/{{name}}` and reports
// the names it found, in the order they appear.
//
// WHAT IS NOT A PATH VARIABLE is the load-bearing half, and startsPathVariable
// is where it is decided: the scheme's own colon is followed by `//` and a
// port is digits, so requiring a letter or `_` immediately after a `/:`
// excludes both. Without that, every ordinary `https://host:8443/x` would be
// rewritten into nonsense and reported as a change. The name runs to the next
// `/`, `?`, `#` or the end — the characters a path segment ends at.
func rewriteColonSegments(rawURL string) (string, []string) {
	var b strings.Builder
	var found []string
	for i := 0; i < len(rawURL); i++ {
		if !startsPathVariable(rawURL, i) {
			b.WriteByte(rawURL[i])
			continue
		}
		// rawURL[i] is the '/', rawURL[i+1] is the ':'.
		b.WriteByte('/')
		j := i + 2
		for j < len(rawURL) && !endsPathSegment(rawURL[j]) {
			j++
		}
		name := rawURL[i+2 : j]
		found = append(found, name)
		b.WriteString("{{")
		b.WriteString(name)
		b.WriteString("}}")
		i = j - 1
	}
	if len(found) == 0 {
		return rawURL, nil
	}
	return b.String(), found
}

// startsPathVariable reports whether a `/:name` begins at i.
func startsPathVariable(rawURL string, i int) bool {
	if i+2 >= len(rawURL) || rawURL[i] != '/' || rawURL[i+1] != ':' {
		return false
	}
	ch := rawURL[i+2]
	return ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

// endsPathSegment reports the characters a path variable's name stops at.
func endsPathSegment(ch byte) bool {
	return ch == '/' || ch == '?' || ch == '#'
}

// assembleURL rebuilds a URL from Postman's parts, for the exports that
// carry no raw. host and path are arrays of strings, and each is also
// allowed to be one string.
func assembleURL(u pmURL) string {
	host := strings.Join(stringList(u.Host), ".")
	segs := stringList(u.Path)
	var b strings.Builder
	if p := u.Protocol.String(); p != "" {
		b.WriteString(p)
		b.WriteString("://")
	}
	b.WriteString(host)
	if p := u.Port.String(); p != "" {
		b.WriteByte(':')
		b.WriteString(p)
	}
	for _, s := range segs {
		b.WriteByte('/')
		b.WriteString(s)
	}
	return b.String()
}

func stringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []pmString
	if err := json.Unmarshal(raw, &list); err == nil {
		out := make([]string, 0, len(list))
		for _, s := range list {
			out = append(out, s.String())
		}
		return out
	}
	var one pmString
	if err := json.Unmarshal(raw, &one); err == nil && one != "" {
		return []string{one.String()}
	}
	return nil
}

func (c *pmConv) readBody(b *pmBody, item string) apicoll.Body {
	if b.Disabled {
		c.itemise("disabled body on "+item, "the model has no disabled state for a body; a request either has one or does not")
		return apicoll.Body{Kind: apicoll.BodyNone}
	}
	switch strings.ToLower(b.Mode) {
	case "", "none":
		return apicoll.Body{Kind: apicoll.BodyNone}

	case "raw":
		return apicoll.Body{Kind: apicoll.BodyRaw, Text: b.Raw.String()}

	case "urlencoded":
		return apicoll.Body{Kind: apicoll.BodyForm, Text: encodeFormParams(b.Urlencoded)}

	case "formdata":
		var files int
		for _, p := range b.Formdata {
			if strings.EqualFold(p.Type, "file") || p.Src != "" {
				files++
				c.itemise("multipart file part "+clip(p.Key.String())+" on "+item,
					"it names a local file to upload; the model has no multipart part that reads one, and an import reads no file its input names")
			}
		}
		c.itemise("multipart body on "+item,
			"it was converted to a urlencoded body, which is a different Content-Type: the text fields are carried and the multipart framing is not")
		text := encodeFormParams(filterTextParts(b.Formdata))
		return apicoll.Body{Kind: apicoll.BodyForm, Text: text}

	case "file":
		src := ""
		if b.File != nil {
			src = b.File.Src.String()
		}
		if src == "" {
			c.itemise("file body on "+item, "it names no file")
			return apicoll.Body{Kind: apicoll.BodyNone}
		}
		return apicoll.Body{Kind: apicoll.BodyFile, FileRef: src}

	case "graphql":
		// A GraphQL body IS this JSON on the wire, so carrying it as raw
		// loses nothing: it is a projection, not a degrade.
		var gql struct {
			Query     pmString `json:"query"`
			Variables pmString `json:"variables"`
			Operation pmString `json:"operationName"`
		}
		if err := json.Unmarshal(b.GraphQL, &gql); err != nil {
			c.itemise("graphql body on "+item, "it could not be read: "+err.Error())
			return apicoll.Body{Kind: apicoll.BodyNone}
		}
		payload := map[string]any{"query": gql.Query.String()}
		if v := strings.TrimSpace(gql.Variables.String()); v != "" {
			var parsed any
			if json.Unmarshal([]byte(v), &parsed) == nil {
				payload["variables"] = parsed
			} else {
				payload["variables"] = v
			}
		}
		if op := gql.Operation.String(); op != "" {
			payload["operationName"] = op
		}
		text, err := json.Marshal(payload)
		if err != nil {
			c.itemise("graphql body on "+item, "it could not be rewritten as a JSON body: "+err.Error())
			return apicoll.Body{Kind: apicoll.BodyNone}
		}
		return apicoll.Body{Kind: apicoll.BodyRaw, Text: string(text)}

	default:
		c.itemise("body mode "+clip(b.Mode)+" on "+item, "it is not a body mode this importer knows; the request was imported without a body")
		return apicoll.Body{Kind: apicoll.BodyNone}
	}
}

func filterTextParts(parts []pmFormParam) []pmFormParam {
	out := parts[:0:0]
	for _, p := range parts {
		if strings.EqualFold(p.Type, "file") || p.Src != "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// encodeFormParams writes the pairs in the order they were given: sorting
// them would change the body on the wire for a server that cares, and a
// diff for one that does not.
func encodeFormParams(parts []pmFormParam) string {
	var enc []string
	for _, p := range parts {
		if p.Disabled {
			continue
		}
		name := p.Key.String()
		if name == "" {
			continue
		}
		enc = append(enc, url.QueryEscape(name)+"="+url.QueryEscape(p.Value.String()))
	}
	return strings.Join(enc, "&")
}
