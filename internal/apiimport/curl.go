package apiimport

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/shady2k/nocx/internal/apicoll"
)

// FromCurl converts one curl command line into a request.
//
// The line is PARSED, never executed (design §10): see tokenize for the
// quoting rules and TestPackageNeverExecs for the assertion that no exec
// exists to reach.
//
// Any credential the line carries — a Bearer token, a -u password, a
// secret-shaped header — becomes a VARIABLE NAME in the returned request,
// and the value is dropped on the floor here, because this function has
// nowhere safe to put it. ImportInto is the entry point that holds a
// BindWriter and therefore the one that offers the value to the binding
// store; a request converted through FromCurl alone names a variable
// nobody has bound yet, which the send reports as unresolved rather than
// sending empty (§6.5).
//
// Everything the bounded flag set does not carry comes back in
// []Unsupported. Nothing is only logged.
func FromCurl(line string) (apicoll.Request, []Unsupported, error) {
	req, _, unsup, err := parseCurl(line, newVarNamer())
	return req, unsup, err
}

// curlFlag describes one flag we recognise.
type curlFlag struct {
	long    string // canonical spelling, with the leading --
	arg     bool   // takes a value
	carried bool   // lands in the model; if false it is itemised
	why     string // the Why for the Unsupported entry when !carried
}

const (
	whyTransport = "a transport option: the send's HTTP policy owns this, not the request (design §7.3)"
	whyMeaning   = "refused: it changes the meaning of the request, and a silently dropped one is worse than a refused one"
	whyOutput    = "an output option: it does not change the request that is sent"
	whyFile      = "it names a local file to read, and an import reads no file its input names"
)

// curlFlags is the bounded set of design §10 plus the flags we recognise
// only well enough to know whether they eat the next token — without that
// second group, `--cacert ca.pem https://x` would take ca.pem for the URL.
var curlFlags = map[string]curlFlag{
	// Carried into the model.
	"--request":        {"--request", true, true, ""},
	"--header":         {"--header", true, true, ""},
	"--data":           {"--data", true, true, ""},
	"--data-raw":       {"--data-raw", true, true, ""},
	"--data-binary":    {"--data-binary", true, true, ""},
	"--data-urlencode": {"--data-urlencode", true, true, ""},
	"--form":           {"--form", true, true, ""},
	"--json":           {"--json", true, true, ""},
	"--user":           {"--user", true, true, ""},
	"--cookie":         {"--cookie", true, true, ""},
	"--get":            {"--get", false, true, ""},
	"--url":            {"--url", true, true, ""},

	// Recognised, and named out loud. The model has no per-request field
	// for these three; the sender's policy decides them.
	"--location":   {"--location", false, false, whyTransport},
	"--insecure":   {"--insecure", false, false, whyTransport},
	"--compressed": {"--compressed", false, false, whyTransport},

	// Refused: each changes what goes on the wire or where it goes.
	"--proxy":         {"--proxy", true, false, whyMeaning},
	"--preproxy":      {"--preproxy", true, false, whyMeaning},
	"--proxy-user":    {"--proxy-user", true, false, whyMeaning},
	"--proxy-header":  {"--proxy-header", true, false, whyMeaning},
	"--socks5":        {"--socks5", true, false, whyMeaning},
	"--socks4":        {"--socks4", true, false, whyMeaning},
	"--cert":          {"--cert", true, false, whyMeaning},
	"--cert-type":     {"--cert-type", true, false, whyMeaning},
	"--key":           {"--key", true, false, whyMeaning},
	"--key-type":      {"--key-type", true, false, whyMeaning},
	"--cacert":        {"--cacert", true, false, whyMeaning},
	"--capath":        {"--capath", true, false, whyMeaning},
	"--pubkey":        {"--pubkey", true, false, whyMeaning},
	"--resolve":       {"--resolve", true, false, whyMeaning},
	"--interface":     {"--interface", true, false, whyMeaning},
	"--unix-socket":   {"--unix-socket", true, false, whyMeaning},
	"--oauth2-bearer": {"--oauth2-bearer", true, false, whyMeaning},
	"--aws-sigv4":     {"--aws-sigv4", true, false, whyMeaning},
	"--negotiate":     {"--negotiate", false, false, whyMeaning},
	"--ntlm":          {"--ntlm", false, false, whyMeaning},
	"--digest":        {"--digest", false, false, whyMeaning},
	"--anyauth":       {"--anyauth", false, false, whyMeaning},
	"--netrc":         {"--netrc", false, false, whyMeaning},
	"--netrc-file":    {"--netrc-file", true, false, whyMeaning},
	"--head":          {"--head", false, false, whyMeaning},
	"--next":          {"--next", false, false, "refused: it starts a second request, and an import produces one"},
	"--form-string":   {"--form-string", true, false, whyMeaning},
	"--user-agent":    {"--user-agent", true, false, whyMeaning},
	"--referer":       {"--referer", true, false, whyMeaning},
	"--max-redirs":    {"--max-redirs", true, false, whyTransport},
	"--proto":         {"--proto", true, false, whyTransport},
	"--tls-max":       {"--tls-max", true, false, whyTransport},
	"--http1.0":       {"--http1.0", false, false, whyTransport},
	"--http1.1":       {"--http1.1", false, false, whyTransport},
	"--http2":         {"--http2", false, false, whyTransport},
	"--ipv4":          {"--ipv4", false, false, whyTransport},
	"--ipv6":          {"--ipv6", false, false, whyTransport},
	"--tlsv1.2":       {"--tlsv1.2", false, false, whyTransport},
	"--tlsv1.3":       {"--tlsv1.3", false, false, whyTransport},

	// Timings and retries: transport policy, not the request.
	"--connect-timeout": {"--connect-timeout", true, false, whyTransport},
	"--max-time":        {"--max-time", true, false, whyTransport},
	"--retry":           {"--retry", true, false, whyTransport},
	"--retry-delay":     {"--retry-delay", true, false, whyTransport},
	"--retry-max-time":  {"--retry-max-time", true, false, whyTransport},
	"--limit-rate":      {"--limit-rate", true, false, whyTransport},
	"--speed-limit":     {"--speed-limit", true, false, whyTransport},
	"--speed-time":      {"--speed-time", true, false, whyTransport},

	// Files we will not read or write.
	"--output":       {"--output", true, false, whyFile},
	"--output-dir":   {"--output-dir", true, false, whyFile},
	"--remote-name":  {"--remote-name", false, false, whyFile},
	"--upload-file":  {"--upload-file", true, false, whyFile},
	"--cookie-jar":   {"--cookie-jar", true, false, whyFile},
	"--config":       {"--config", true, false, whyFile},
	"--dump-header":  {"--dump-header", true, false, whyFile},
	"--trace":        {"--trace", true, false, whyFile},
	"--trace-ascii":  {"--trace-ascii", true, false, whyFile},
	"--continue-at":  {"--continue-at", true, false, whyFile},
	"--create-dirs":  {"--create-dirs", false, false, whyFile},
	"--remote-time":  {"--remote-time", false, false, whyFile},
	"--range":        {"--range", true, false, whyMeaning},
	"--time-cond":    {"--time-cond", true, false, whyMeaning},
	"--write-out":    {"--write-out", true, false, whyOutput},
	"--silent":       {"--silent", false, false, whyOutput},
	"--show-error":   {"--show-error", false, false, whyOutput},
	"--verbose":      {"--verbose", false, false, whyOutput},
	"--include":      {"--include", false, false, whyOutput},
	"--fail":         {"--fail", false, false, whyOutput},
	"--progress-bar": {"--progress-bar", false, false, whyOutput},
	"--no-buffer":    {"--no-buffer", false, false, whyOutput},
	"--globoff":      {"--globoff", false, false, whyOutput},
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
// see.
func parseCurl(line string, namer *varNamer) (apicoll.Request, []secretOffer, []Unsupported, error) {
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
		if !f.carried {
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
// in that case the credential is dropped rather than written anywhere,
// because there is no field in which a file may spell one (§8).
func authFromHeader(value string, namer *varNamer) (apicoll.Auth, *secretOffer, string) {
	value = strings.TrimSpace(value)
	scheme, cred, _ := strings.Cut(value, " ")
	cred = strings.TrimSpace(cred)
	switch strings.ToLower(scheme) {
	case "bearer":
		if name, ok := varRef(cred); ok {
			namer.reserve(name)
			return apicoll.Auth{Kind: apicoll.AuthBearer, Var: name}, nil, ""
		}
		v := namer.take("token")
		return apicoll.Auth{Kind: apicoll.AuthBearer, Var: v}, &secretOffer{Variable: v, Value: []byte(cred)}, ""
	case "basic":
		if name, ok := varRef(cred); ok {
			namer.reserve(name)
			return apicoll.Auth{Kind: apicoll.AuthBasic, Var: name}, nil, ""
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
		return apicoll.Auth{Kind: apicoll.AuthBasic, User: user, Var: v}, &secretOffer{Variable: v, Value: []byte(pass)}, ""
	default:
		if scheme == "" {
			return apicoll.Auth{}, nil, "(empty)"
		}
		return apicoll.Auth{}, nil, scheme
	}
}

// basicFromUserArg maps -u. A -u with no colon is curl's "prompt me": the
// variable is named and left unbound, so the send blocks and says which
// variable is missing instead of sending an empty password.
func basicFromUserArg(arg string, namer *varNamer) (apicoll.Auth, *secretOffer) {
	user, pass, ok := strings.Cut(arg, ":")
	auth := apicoll.Auth{Kind: apicoll.AuthBasic, User: user}
	if !ok {
		auth.Var = namer.take("password")
		return auth, nil
	}
	if name, isRef := varRef(pass); isRef {
		namer.reserve(name)
		auth.Var = name
		return auth, nil
	}
	v := namer.take("password")
	auth.Var = v
	return auth, &secretOffer{Variable: v, Value: []byte(pass)}
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
