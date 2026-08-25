package apiimport

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/shady2k/nocx/internal/apicoll"
)

// FromCurl converts one curl command line into a request, for the FORM.
//
// The line is PARSED, never executed (design §10): see tokenize for the
// quoting rules and TestPackageNeverExecs for the assertion that no exec
// exists to reach.
//
// # It mints no variable it cannot bind (nocx-14exx)
//
// This entrance writes no file and holds no BindWriter: what it returns
// goes straight back to the person who pasted the line, into the request
// form, with no collection and no environment behind it yet. It therefore
// leaves a credential the line carries exactly where the line put it — in
// the Authorization header, in the -u argument — and names no variable for
// it.
//
// It used to do the opposite: an Authorization header became
// Auth{bearer, Var: "token"} and the value was dropped on the floor, so the
// person got a request naming a variable that had nowhere to be bound, no
// Variables row to bind it in, and a Send that refused with `the auth
// variable "token" is not bound in this environment` about a name they had
// never chosen. The credential was not protected by that, only lost; the
// only thing §8 protects is a FILE, and this entrance writes none.
// ImportInto is the entrance that writes files, and there the old
// behaviour is still exactly right — see parseCurl's credentials argument.
//
// Everything the bounded flag set does not carry comes back in
// []Unsupported. Nothing is only logged.
func FromCurl(line string) (apicoll.Request, []Unsupported, error) {
	req, _, unsup, err := parseCurl(line, newVarNamer(), credentialsStayOnRequest)
	return req, unsup, err
}

// credentials says where a credential the curl line carries is allowed to
// go, which is a property of THE CALLER and not of the line.
//
// Two entrances, two answers, one converter (§10). Getting this wrong in
// either direction is a defect with a name: a collection file that spells a
// token is what §8 exists to prevent, and a form that names an unbindable
// variable is nocx-14exx.
type credentials int

const (
	// credentialsToBinder is the ImportInto route: a collection is being
	// written, so a credential becomes a VARIABLE NAME in the files and the
	// value goes to the BindWriter and nowhere else (§8). It is the ZERO
	// value deliberately — a caller that forgets this argument gets the
	// answer that never writes a secret to disk.
	credentialsToBinder credentials = iota
	// credentialsStayOnRequest is the FromCurl route: nothing here can bind
	// a value, so nothing here mints a name for one. A variable the LINE
	// ITSELF spells — `Authorization: Bearer {{tok}}` — is still honoured,
	// because that name is the person's own and not one we invented.
	credentialsStayOnRequest
)

// disposition is what becomes of one recognised flag. THREE STATES, NOT
// TWO, and the third is the whole of nocx-q2cx5's first half.
//
// "Itemise everything we do not carry" was one rule doing two jobs. A
// refusal is worth a row because the request that comes out is NOT the line
// that went in — -k, -L and -o each change what happens on the wire or to
// the answer. `-s` and `-S` change neither: they are how a person keeps
// curl quiet in a shell, they are on almost every line anybody pastes, and
// reporting them made the ordinary import arrive with a warning list that
// said nothing. A list that is noisy on every line is one nobody reads on
// the line where it matters, which costs exactly the visibility AGENTS.md's
// "a soft degrade must be visible in the product" is asking for.
//
// So the test is not "did we carry it" but CAN THIS FLAG CHANGE THE REQUEST
// THAT IS SENT. If it cannot, there is no degrade to be visible about.
type disposition int

const (
	// dispCarried lands in the model. It is the zero value so that a row
	// added without thinking about this column is one whose flag is read,
	// rather than one silently dropped.
	dispCarried disposition = iota
	// dispIgnored cannot change the request that is sent. Recognised only
	// so it does not eat the next token, and reported nowhere.
	dispIgnored
	// dispItemised names the flag in Unsupported with its Why. Never its
	// argument: a refused --oauth2-bearer would otherwise itemise the token
	// it refused.
	dispItemised
)

// curlFlag describes one flag we recognise.
type curlFlag struct {
	long string      // canonical spelling, with the leading --
	arg  bool        // takes a value
	disp disposition // carried, ignored, or itemised
	why  string      // the Why for the Unsupported entry, when itemised
}

const (
	whyTransport = "a transport option: the send's HTTP policy owns this, not the request (design §7.3)"
	whyMeaning   = "refused: it changes the meaning of the request, and a silently dropped one is worse than a refused one"
	whyFile      = "it names a local file to read, and an import reads no file its input names"
)

// curlFlags is the bounded set of design §10 plus the flags we recognise
// only well enough to know whether they eat the next token — without that
// second group, `--cacert ca.pem https://x` would take ca.pem for the URL.
var curlFlags = map[string]curlFlag{
	// Carried into the model.
	"--request":        {"--request", true, dispCarried, ""},
	"--header":         {"--header", true, dispCarried, ""},
	"--data":           {"--data", true, dispCarried, ""},
	"--data-raw":       {"--data-raw", true, dispCarried, ""},
	"--data-binary":    {"--data-binary", true, dispCarried, ""},
	"--data-urlencode": {"--data-urlencode", true, dispCarried, ""},
	"--form":           {"--form", true, dispCarried, ""},
	"--json":           {"--json", true, dispCarried, ""},
	"--user":           {"--user", true, dispCarried, ""},
	"--cookie":         {"--cookie", true, dispCarried, ""},
	"--get":            {"--get", false, dispCarried, ""},
	"--url":            {"--url", true, dispCarried, ""},

	// Recognised, and named out loud. The model has no per-request field
	// for these three; the sender's policy decides them.
	"--location":   {"--location", false, dispItemised, whyTransport},
	"--insecure":   {"--insecure", false, dispItemised, whyTransport},
	"--compressed": {"--compressed", false, dispItemised, whyTransport},

	// Refused: each changes what goes on the wire or where it goes.
	"--proxy":         {"--proxy", true, dispItemised, whyMeaning},
	"--preproxy":      {"--preproxy", true, dispItemised, whyMeaning},
	"--proxy-user":    {"--proxy-user", true, dispItemised, whyMeaning},
	"--proxy-header":  {"--proxy-header", true, dispItemised, whyMeaning},
	"--socks5":        {"--socks5", true, dispItemised, whyMeaning},
	"--socks4":        {"--socks4", true, dispItemised, whyMeaning},
	"--cert":          {"--cert", true, dispItemised, whyMeaning},
	"--cert-type":     {"--cert-type", true, dispItemised, whyMeaning},
	"--key":           {"--key", true, dispItemised, whyMeaning},
	"--key-type":      {"--key-type", true, dispItemised, whyMeaning},
	"--cacert":        {"--cacert", true, dispItemised, whyMeaning},
	"--capath":        {"--capath", true, dispItemised, whyMeaning},
	"--pubkey":        {"--pubkey", true, dispItemised, whyMeaning},
	"--resolve":       {"--resolve", true, dispItemised, whyMeaning},
	"--interface":     {"--interface", true, dispItemised, whyMeaning},
	"--unix-socket":   {"--unix-socket", true, dispItemised, whyMeaning},
	"--oauth2-bearer": {"--oauth2-bearer", true, dispItemised, whyMeaning},
	"--aws-sigv4":     {"--aws-sigv4", true, dispItemised, whyMeaning},
	"--negotiate":     {"--negotiate", false, dispItemised, whyMeaning},
	"--ntlm":          {"--ntlm", false, dispItemised, whyMeaning},
	"--digest":        {"--digest", false, dispItemised, whyMeaning},
	"--anyauth":       {"--anyauth", false, dispItemised, whyMeaning},
	"--netrc":         {"--netrc", false, dispItemised, whyMeaning},
	"--netrc-file":    {"--netrc-file", true, dispItemised, whyMeaning},
	"--head":          {"--head", false, dispItemised, whyMeaning},
	"--next":          {"--next", false, dispItemised, "refused: it starts a second request, and an import produces one"},
	"--form-string":   {"--form-string", true, dispItemised, whyMeaning},
	"--user-agent":    {"--user-agent", true, dispItemised, whyMeaning},
	"--referer":       {"--referer", true, dispItemised, whyMeaning},
	"--max-redirs":    {"--max-redirs", true, dispItemised, whyTransport},
	"--proto":         {"--proto", true, dispItemised, whyTransport},
	"--tls-max":       {"--tls-max", true, dispItemised, whyTransport},
	"--http1.0":       {"--http1.0", false, dispItemised, whyTransport},
	"--http1.1":       {"--http1.1", false, dispItemised, whyTransport},
	"--http2":         {"--http2", false, dispItemised, whyTransport},
	"--ipv4":          {"--ipv4", false, dispItemised, whyTransport},
	"--ipv6":          {"--ipv6", false, dispItemised, whyTransport},
	"--tlsv1.2":       {"--tlsv1.2", false, dispItemised, whyTransport},
	"--tlsv1.3":       {"--tlsv1.3", false, dispItemised, whyTransport},

	// Timings and retries: transport policy, not the request.
	"--connect-timeout": {"--connect-timeout", true, dispItemised, whyTransport},
	"--max-time":        {"--max-time", true, dispItemised, whyTransport},
	"--retry":           {"--retry", true, dispItemised, whyTransport},
	"--retry-delay":     {"--retry-delay", true, dispItemised, whyTransport},
	"--retry-max-time":  {"--retry-max-time", true, dispItemised, whyTransport},
	"--limit-rate":      {"--limit-rate", true, dispItemised, whyTransport},
	"--speed-limit":     {"--speed-limit", true, dispItemised, whyTransport},
	"--speed-time":      {"--speed-time", true, dispItemised, whyTransport},

	// Files we will not read or write.
	"--output":      {"--output", true, dispItemised, whyFile},
	"--output-dir":  {"--output-dir", true, dispItemised, whyFile},
	"--remote-name": {"--remote-name", false, dispItemised, whyFile},
	"--upload-file": {"--upload-file", true, dispItemised, whyFile},
	"--cookie-jar":  {"--cookie-jar", true, dispItemised, whyFile},
	"--config":      {"--config", true, dispItemised, whyFile},
	"--dump-header": {"--dump-header", true, dispItemised, whyFile},
	"--trace":       {"--trace", true, dispItemised, whyFile},
	"--trace-ascii": {"--trace-ascii", true, dispItemised, whyFile},
	"--continue-at": {"--continue-at", true, dispItemised, whyFile},
	"--create-dirs": {"--create-dirs", false, dispItemised, whyFile},
	"--remote-time": {"--remote-time", false, dispItemised, whyFile},
	"--range":       {"--range", true, dispItemised, whyMeaning},
	"--time-cond":   {"--time-cond", true, dispItemised, whyMeaning},

	// Ignored in silence: every one of these governs what CURL PRINTS or
	// how it exits, and none of them can change the bytes that go out or
	// the address they go to. `curl -sS …` is the ordinary way a person
	// writes a line they mean to read the output of, and an import of it
	// reports nothing, because nothing was lost. -g is here for a
	// different reason with the same answer: it turns curl's URL globbing
	// OFF, and this importer never globbed, so the flag asks for what it
	// already gets. -w takes a value and must stay in the table to eat it.
	"--write-out":    {"--write-out", true, dispIgnored, ""},
	"--silent":       {"--silent", false, dispIgnored, ""},
	"--show-error":   {"--show-error", false, dispIgnored, ""},
	"--verbose":      {"--verbose", false, dispIgnored, ""},
	"--include":      {"--include", false, dispIgnored, ""},
	"--fail":         {"--fail", false, dispIgnored, ""},
	"--progress-bar": {"--progress-bar", false, dispIgnored, ""},
	"--no-buffer":    {"--no-buffer", false, dispIgnored, ""},
	"--globoff":      {"--globoff", false, dispIgnored, ""},
}

// curlShorts maps a short flag to its canonical long spelling. A letter
// absent here is unknown, and unknown is itemised rather than guessed at.
var curlShorts = map[byte]string{
	'X': "--request", 'H': "--header", 'd': "--data", 'F': "--form",
	'u': "--user", 'b': "--cookie", 'G': "--get", 'L': "--location",
	'k': "--insecure",
	'x': "--proxy", 'E': "--cert", 'o': "--output", 'O': "--remote-name",
	'A': "--user-agent", 'e': "--referer", 'c': "--cookie-jar",
	'C': "--continue-at", 'D': "--dump-header", 'K': "--config",
	'm': "--max-time", 'r': "--range", 'T': "--upload-file",
	'w': "--write-out", 'y': "--speed-time", 'Y': "--speed-limit",
	'z': "--time-cond", 'U': "--proxy-user", 's': "--silent",
	'S': "--show-error", 'v': "--verbose", 'i': "--include",
	'I': "--head", 'f': "--fail", '#': "--progress-bar",
	'g': "--globoff", 'N': "--no-buffer", '0': "--http1.0",
	'6': "--ipv6", '4': "--ipv4", 'R': "--remote-time",
}

// dataPart is one -d/--data-*/-F argument, kept in order because curl joins
// them in order and the body a person expects is the one their line makes.
type dataPart struct {
	kind  string // "raw", "file", "pair"
	text  string // raw text
	name  string // pair name
	value string // pair value, decoded
	ref   string // file reference
}

// parseCurl is FromCurl plus the secret values, which only ImportInto may
// see. creds decides whether there is anywhere for them to go at all.
func parseCurl(line string, namer *varNamer, creds credentials) (apicoll.Request, []secretOffer, []Unsupported, error) {
	var (
		req     apicoll.Request
		offers  []secretOffer
		unsup   []Unsupported
		headers []apicoll.Header
		parts   []dataPart
		urls    []string
		method  string
		getFlag bool
		jsonAdd bool
		userArg string
		hasUser bool
	)
	itemise := func(what, why string) { unsup = append(unsup, Unsupported{What: what, Why: why}) }

	toks, err := tokenize(line)
	if err != nil {
		return req, nil, nil, err
	}
	// A pasted line often carries its prompt.
	for len(toks) > 0 && (toks[0] == "$" || toks[0] == "#" || toks[0] == ">") {
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return req, nil, nil, errors.New("apiimport: empty command line")
	}
	if base := strings.ToLower(path.Base(toks[0])); base != "curl" && base != "curl.exe" {
		return req, nil, nil, fmt.Errorf("apiimport: not a curl command line (starts with %q)", ellipsis(toks[0], 60))
	}

	// handle applies one recognised flag. value is meaningful only when the
	// flag takes one.
	handle := func(f curlFlag, value string) {
		switch f.disp {
		case dispIgnored:
			return
		case dispItemised:
			itemise(f.long, f.why)
			return
		}
		switch f.long {
		case "--request":
			method = strings.ToUpper(strings.TrimSpace(value))
		case "--header":
			if h, ok := parseHeaderArg(value); ok {
				headers = append(headers, h)
			} else {
				itemise("-H "+headerNameOf(value), "the header argument has no name")
			}
		case "--data", "--data-binary":
			if strings.HasPrefix(value, "@") {
				parts = append(parts, dataPart{kind: "file", ref: strings.TrimPrefix(value, "@")})
			} else {
				parts = append(parts, dataPart{kind: "raw", text: value})
			}
		case "--data-raw":
			parts = append(parts, dataPart{kind: "raw", text: value})
		case "--data-urlencode":
			name, val, ok := splitURLEncodeArg(value)
			if !ok {
				itemise("--data-urlencode name@file", whyFile)
				return
			}
			parts = append(parts, dataPart{kind: "pair", name: name, value: val})
		case "--form":
			name, val, ok := splitFormArg(value)
			if !ok {
				itemise("-F file part", whyFile)
				return
			}
			parts = append(parts, dataPart{kind: "pair", name: name, value: val})
		case "--json":
			parts = append(parts, dataPart{kind: "raw", text: value})
			jsonAdd = true
		case "--user":
			userArg, hasUser = value, true
		case "--cookie":
			if strings.Contains(value, "=") {
				headers = append(headers, apicoll.Header{Name: "Cookie", Value: value, Enabled: true})
			} else {
				itemise("-b cookie file", whyFile)
			}
		case "--get":
			getFlag = true
		case "--url":
			urls = append(urls, value)
		}
	}

	for i := 1; i < len(toks); i++ {
		tok := toks[i]
		switch {
		case strings.HasPrefix(tok, "--"):
			name, inline, hasInline := strings.Cut(tok, "=")
			f, known := curlFlags[name]
			if !known {
				// An unknown long flag is assumed not to take a value: a
				// wrong guess in the other direction would silently eat the
				// URL.
				itemise(name, "not a flag this importer knows; nothing was assumed about it")
				continue
			}
			value := inline
			if f.arg && !hasInline {
				if i+1 >= len(toks) {
					return req, nil, nil, fmt.Errorf("apiimport: %s expects a value", name)
				}
				i++
				value = toks[i]
			}
			handle(f, value)

		case len(tok) > 1 && tok[0] == '-':
			// A bundle: -sSL, and -XPOST where the value is attached.
			rest := tok[1:]
			for len(rest) > 0 {
				c := rest[0]
				rest = rest[1:]
				long, known := curlShorts[c]
				if !known {
					itemise("-"+string(c), "not a flag this importer knows; nothing was assumed about it")
					continue
				}
				f := curlFlags[long]
				if !f.arg {
					handle(f, "")
					continue
				}
				value := rest
				rest = ""
				if value == "" {
					if i+1 >= len(toks) {
						return req, nil, nil, fmt.Errorf("apiimport: -%s expects a value", string(c))
					}
					i++
					value = toks[i]
				}
				handle(f, value)
			}

		default:
			urls = append(urls, tok)
		}
	}

	if len(urls) == 0 {
		return req, nil, nil, errors.New("apiimport: the curl line names no URL")
	}
	for range urls[1:] {
		itemise("second URL", "curl takes several URLs and an imported request is one; only the first was kept")
	}

	base, query := splitQuery(urls[0])
	req.URL = base
	req.Query = query

	// --json adds two headers, and only where the line did not set them
	// itself: curl does the same, and without them the server does
	// something else with the body.
	if jsonAdd {
		for _, name := range []string{"Content-Type", "Accept"} {
			if !hasHeader(headers, name) {
				headers = append(headers, apicoll.Header{Name: name, Value: "application/json", Enabled: true})
			}
		}
	}

	// -u first, so an explicit Authorization header still wins — which is
	// the order curl resolves them in.
	switch creds {
	case credentialsToBinder:
		if hasUser {
			auth, offer := basicFromUserArg(userArg, namer)
			req.Auth = auth
			if offer != nil {
				offers = append(offers, *offer)
			}
		}
		kept, headerAuth, headerOffers, headerUnsup := absorbHeaderSecrets(headers, namer)
		req.Headers = kept
		offers = append(offers, headerOffers...)
		unsup = append(unsup, headerUnsup...)
		if headerAuth != nil {
			req.Auth = *headerAuth
		}

	case credentialsStayOnRequest:
		// EVERY HEADER THE LINE CARRIES, IN THE ORDER IT GAVE THEM. There
		// is no absorption here and no secret detector: a header is a
		// header, which is the only shape in which the request that comes
		// out is the command that went in (nocx-9jnu6).
		req.Headers = headers
		if hasUser {
			auth, header, why := userArgOnRequest(userArg, hasHeader(headers, "Authorization"))
			switch {
			case why != "":
				itemise("-u without a password", why)
			case auth != nil:
				req.Auth = *auth
			case header != nil:
				// After the line's own headers: curl generates this one
				// itself, so it was never among them, and appending keeps
				// the order the person wrote.
				req.Headers = append(req.Headers, *header)
			}
		}
	}

	// -G moves the data to the query rather than sending it as a body.
	if getFlag {
		for _, p := range parts {
			switch p.kind {
			case "pair":
				req.Query = append(req.Query, apicoll.Param{Name: p.name, Value: p.value, Enabled: true})
			case "raw":
				req.Query = append(req.Query, rawToParams(p.text)...)
			case "file":
				itemise("-G with -d @file", whyFile)
			}
		}
		parts = nil
	}

	body, bodyUnsup := assembleBody(parts)
	req.Body = body
	unsup = append(unsup, bodyUnsup...)
	// THE MODE THE LINE ALREADY NAMES. assembleBody knows what the -d
	// arguments were shaped like and nothing about the headers, so the JSON
	// question is answered here, where both halves are in hand.
	//
	// Raw declares nothing (apicoll.Body): it is the mode for a body whose
	// format only the user's own Content-Type header states. A line that
	// says `Content-Type: application/json`, or whose payload simply IS a
	// JSON document, has stated it — so leaving that request in raw mode
	// made the person open the body tab and pick the mode their own line
	// had already named, and cost the editor above it the highlighting it
	// would then have known to do.
	if req.Body.Kind == apicoll.BodyRaw && (headersNameJSON(req.Headers) || isJSONDocument(req.Body.Text)) {
		req.Body.Kind = apicoll.BodyJSON
	}

	switch {
	case method != "":
		req.Method = method
	case getFlag:
		req.Method = "GET"
	case req.Body.Kind != apicoll.BodyNone:
		req.Method = "POST"
	default:
		req.Method = "GET"
	}

	req.Name = requestNameFor(req.Method, req.URL)
	return req, offers, unsup, nil
}

// assembleBody turns the -d/-F parts into the one body the model holds.
func assembleBody(parts []dataPart) (apicoll.Body, []Unsupported) {
	if len(parts) == 0 {
		return apicoll.Body{Kind: apicoll.BodyNone}, nil
	}
	var (
		unsup  []Unsupported
		files  []string
		pairs  int
		rawTxt []string
		enc    []string
	)
	for _, p := range parts {
		switch p.kind {
		case "file":
			files = append(files, p.ref)
		case "pair":
			pairs++
			enc = append(enc, url.QueryEscape(p.name)+"="+url.QueryEscape(p.value))
		case "raw":
			rawTxt = append(rawTxt, p.text)
			enc = append(enc, p.text)
		}
	}
	if len(files) == 1 && pairs == 0 && len(rawTxt) == 0 {
		return apicoll.Body{Kind: apicoll.BodyFile, FileRef: files[0]}, nil
	}
	for _, f := range files {
		_ = f
		unsup = append(unsup, Unsupported{
			What: "-d @file alongside other data",
			Why:  "the model holds one body: the inline data was kept and the file reference was not",
		})
	}
	if pairs > 0 {
		return apicoll.Body{Kind: apicoll.BodyForm, Text: strings.Join(enc, "&")}, unsup
	}
	return apicoll.Body{Kind: apicoll.BodyRaw, Text: strings.Join(rawTxt, "&")}, unsup
}

// parseHeaderArg reads one -H argument. "Name;" is curl's spelling for a
// header sent with an empty value.
func parseHeaderArg(arg string) (apicoll.Header, bool) {
	if name, value, ok := strings.Cut(arg, ":"); ok {
		name = strings.TrimSpace(name)
		if name == "" {
			return apicoll.Header{}, false
		}
		return apicoll.Header{Name: name, Value: strings.TrimSpace(value), Enabled: true}, true
	}
	if name, ok := strings.CutSuffix(strings.TrimSpace(arg), ";"); ok && name != "" {
		return apicoll.Header{Name: name, Enabled: true}, true
	}
	return apicoll.Header{}, false
}

// headerNameOf is used only to itemise a malformed -H without echoing the
// argument, which may be the credential.
func headerNameOf(arg string) string {
	if name, _, ok := strings.Cut(arg, ":"); ok {
		return strings.TrimSpace(name)
	}
	return "(unnamed)"
}

func hasHeader(hs []apicoll.Header, name string) bool {
	for _, h := range hs {
		if strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}

// authFromHeader maps an Authorization value onto the model. The third
// return is the scheme we could not map, named so it can be itemised — and
// in that case the credential is dropped rather than written anywhere.
//
// A credential this entrance carries into a FILE is written as a variable
// reference, `{{name}}`, and the value is offered to the BindWriter — a
// file never carries the value's TEXT, which is the whole of §8 (see the
// package doc). The auth field is text like any other (nocx-6hg2w.20): the
// reference is ordinary `{{name}}` text, resolved by the same substitution
// as the URL.
func authFromHeader(value string, namer *varNamer) (apicoll.Auth, *secretOffer, string) {
	value = strings.TrimSpace(value)
	scheme, cred, _ := strings.Cut(value, " ")
	cred = strings.TrimSpace(cred)
	switch strings.ToLower(scheme) {
	case "bearer":
		if name, ok := varRef(cred); ok {
			namer.reserve(name)
			return apicoll.Auth{Kind: apicoll.AuthBearer, Token: "{{" + name + "}}"}, nil, ""
		}
		v := namer.take("token")
		return apicoll.Auth{Kind: apicoll.AuthBearer, Token: "{{" + v + "}}"}, &secretOffer{Variable: v, Value: []byte(cred)}, ""
	case "basic":
		if name, ok := varRef(cred); ok {
			namer.reserve(name)
			return apicoll.Auth{Kind: apicoll.AuthBasic, User: "", Password: "{{" + name + "}}"}, nil, ""
		}
		raw, err := base64.StdEncoding.DecodeString(cred)
		if err != nil {
			return apicoll.Auth{}, nil, "Basic (not decodable)"
		}
		user, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			return apicoll.Auth{}, nil, "Basic (not user:password)"
		}
		v := namer.take("password")
		return apicoll.Auth{Kind: apicoll.AuthBasic, User: user, Password: "{{" + v + "}}"}, &secretOffer{Variable: v, Value: []byte(pass)}, ""
	default:
		if scheme == "" {
			return apicoll.Auth{}, nil, "(empty)"
		}
		return apicoll.Auth{}, nil, scheme
	}
}

// basicFromUserArg maps -u for the entrance that BINDS. A -u with no colon
// is curl's "prompt me": the variable is named and left unbound, so the
// send blocks and says which variable is missing instead of sending an
// empty password.
func basicFromUserArg(arg string, namer *varNamer) (apicoll.Auth, *secretOffer) {
	user, pass, ok := strings.Cut(arg, ":")
	auth := apicoll.Auth{Kind: apicoll.AuthBasic, User: user}
	if !ok {
		auth.Password = "{{" + namer.take("password") + "}}"
		return auth, nil
	}
	if name, isRef := varRef(pass); isRef {
		namer.reserve(name)
		auth.Password = "{{" + name + "}}"
		return auth, nil
	}
	v := namer.take("password")
	auth.Password = "{{" + v + "}}"
	return auth, &secretOffer{Variable: v, Value: []byte(pass)}
}

// userArgOnRequest maps -u for the entrance that binds nothing, and returns
// exactly one of three things: an auth block, a header, or the reason there
// is neither.
//
// hasAuthHeader is curl's own precedence, not ours: `-H "Authorization: …"`
// REPLACES the header -u would have generated, so a line carrying both is a
// line whose -u never reached the wire, and importing it as though it had
// would send a credential curl did not.
//
// The three cases:
//
//   - `-u user:{{pw}}` — the LINE named the variable, so the model's basic
//     auth carries it and the environment answers it. Nothing was minted.
//   - `-u user:password` — the header curl itself would have built. base64
//     is an encoding and not a protection, which is the whole reason
//     apisend.Apply reports the ENCODED value as the placed secret rather
//     than the plaintext.
//   - `-u user` — curl would prompt. An import has nobody to ask, so
//     nothing is carried and the reason is itemised: a request that
//     authenticates as nobody, sent while the person believes it
//     authenticates as them, is the silent degrade AGENTS.md forbids.
func userArgOnRequest(arg string, hasAuthHeader bool) (*apicoll.Auth, *apicoll.Header, string) {
	if hasAuthHeader {
		return nil, nil, ""
	}
	user, pass, ok := strings.Cut(arg, ":")
	if !ok {
		return nil, nil, "curl would have prompted for the password and an import has nobody to ask, " +
			"so no credential was carried: give it in the Auth tab"
	}
	if name, isRef := varRef(pass); isRef {
		return &apicoll.Auth{Kind: apicoll.AuthBasic, User: user, Password: "{{" + name + "}}"}, nil, ""
	}
	return nil, &apicoll.Header{
		Name:    "Authorization",
		Value:   "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)),
		Enabled: true,
	}, ""
}

// headersNameJSON reports whether the line's own Content-Type says JSON.
//
// The media type is what is read — the parameters after the semicolon are
// the charset and the boundary, which say nothing about the format — and
// the `+json` structured suffix counts, because `application/vnd.api+json`
// is a JSON document by the same registry rule that makes
// `application/json` one.
func headersNameJSON(hs []apicoll.Header) bool {
	for _, h := range hs {
		if !strings.EqualFold(h.Name, "Content-Type") {
			continue
		}
		media := strings.ToLower(strings.TrimSpace(h.Value))
		if i := strings.IndexByte(media, ';'); i >= 0 {
			media = strings.TrimSpace(media[:i])
		}
		if media == "application/json" || strings.HasSuffix(media, "+json") {
			return true
		}
	}
	return false
}

// isJSONDocument reports whether the payload IS one — an object or an
// array, and valid.
//
// A bare scalar is deliberately not enough. `-d 42` and `-d true` are valid
// JSON texts and are also what a form field looks like, so accepting them
// would put half the `-d name=value` world into the JSON mode on the
// strength of the one value that has no `=` in it. An object or an array is
// a thing a person wrote as JSON.
func isJSONDocument(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || (t[0] != '{' && t[0] != '[') {
		return false
	}
	return json.Valid([]byte(t))
}

// splitURLEncodeArg reads a --data-urlencode argument. The @ forms name a
// file, which an import does not read.
func splitURLEncodeArg(arg string) (name, value string, ok bool) {
	eq := strings.IndexByte(arg, '=')
	at := strings.IndexByte(arg, '@')
	if at >= 0 && (eq < 0 || at < eq) {
		return "", "", false
	}
	if eq < 0 {
		return "", arg, true
	}
	return arg[:eq], arg[eq+1:], true
}

// splitFormArg reads a -F argument. @file and <file both read a local file.
func splitFormArg(arg string) (name, value string, ok bool) {
	n, v, found := strings.Cut(arg, "=")
	if !found {
		return "", "", false
	}
	if strings.HasPrefix(v, "@") || strings.HasPrefix(v, "<") {
		return "", "", false
	}
	return n, v, true
}

// splitQuery takes the query off a URL and decodes it.
//
// The rule, held to everywhere in this package: text that came out of a URL
// STRING is percent-decoded, and text that came out of a structured field
// is taken verbatim. The model holds what the user means and the sender
// encodes it, so decoding here and not there is what keeps one answer to
// the question. A {{template}} decodes to itself, which is why it survives.
func splitQuery(raw string) (string, []apicoll.Param) {
	base, q, ok := strings.Cut(raw, "?")
	if !ok || q == "" {
		return raw, nil
	}
	return base, rawToParams(q)
}

func rawToParams(q string) []apicoll.Param {
	var out []apicoll.Param
	for _, pair := range strings.Split(q, "&") {
		if pair == "" {
			continue
		}
		name, value, _ := strings.Cut(pair, "=")
		out = append(out, apicoll.Param{
			Name:    queryUnescapeOrLiteral(name),
			Value:   queryUnescapeOrLiteral(value),
			Enabled: true,
		})
	}
	return out
}

// queryUnescapeOrLiteral decodes, and keeps the literal text when the input
// is not valid percent-encoding. A stray % in a pasted URL is far more
// often a stray % than an intent to fail the import.
func queryUnescapeOrLiteral(s string) string {
	if dec, err := url.QueryUnescape(s); err == nil {
		return dec
	}
	return s
}

// requestNameFor gives the request a name a person recognises in a list.
func requestNameFor(method, rawURL string) string {
	seg := ""
	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		seg = path.Base(strings.TrimRight(u.Path, "/"))
	}
	if seg == "" || seg == "." || seg == "/" {
		seg = path.Base(strings.TrimRight(rawURL, "/"))
	}
	if seg == "" || seg == "." || seg == "/" {
		seg = "request"
	}
	return method + " " + seg
}

// ellipsis bounds a fragment of the CALLER'S OWN INPUT quoted back at them.
//
// A refusal quotes what it refused so a person can see which part it meant,
// and the first token of a pasted line is usually short — `wget`, `http`, a
// prompt. It is not always: a URL pasted by mistake is one token and can be
// two kilobytes of query string, and the sentence built from it then arrives
// in a panel as a wall of text with no spaces in it to wrap on. The message
// is for a person to read; sixty runes is as much of a wrong first word as
// anybody needs to recognise it.
//
// Runes, not bytes: cutting a UTF-8 sequence in half would put a replacement
// character in a message about somebody's own text.
func ellipsis(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
