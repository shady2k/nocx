package transport

// connections.hostMode and connections.setHostMode — the connection editor's
// read and write of the HOST's integration method (ADR-727g63tr decision 1).
// These exercise the handlers through the real socket, with the profile
// resolver and the known_hosts index as the two seams the product wires.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
)

// fakeHostModeResolver answers one configured connection, and fails when the
// test says the connection cannot be resolved.
type fakeHostModeResolver struct {
	host  string
	cfg   *ssh.ConnectConfig
	err   error
	asked []string
}

func (f *fakeHostModeResolver) Resolve(profileID string) (string, *ssh.ConnectConfig, error) {
	f.asked = append(f.asked, profileID)
	if f.err != nil {
		return "", nil, f.err
	}
	return f.host, f.cfg, nil
}

// fakeHostKeyIndex answers which fingerprints known_hosts holds for the
// destination it was asked about.
type fakeHostKeyIndex struct {
	fingerprints []string
	err          error
	askedHosts   []string
}

func (f *fakeHostKeyIndex) FingerprintsFor(_ context.Context, host string, _ *ssh.ConnectConfig) ([]string, error) {
	f.askedHosts = append(f.askedHosts, host)
	return f.fingerprints, f.err
}

func startHostModeServer(t *testing.T, modes *hostmode.Store, resolver ProfileResolver, index HostKeyIndex) *WSServer {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	srv := NewWSServer(logger, newRegWithStub(logger), WithHostModeStore(modes), WithHostKeyIndex(index))
	if resolver != nil {
		srv.SetProfileResolver(resolver)
	}
	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(ctx) })
	return srv
}

func hostModeCall(t *testing.T, srv *WSServer, method string, params map[string]any) hostModeResult {
	t.Helper()
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck
	raw := jsonrpcCall(t, conn, method, params)
	var envelope struct {
		Result hostModeResult   `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal %s: %v", method, err)
	}
	if envelope.Error != nil {
		t.Fatalf("%s answered %d: %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	return envelope.Result
}

func hostModeCallError(t *testing.T, srv *WSServer, method string, params map[string]any) *jsonrpcErrorObj {
	t.Helper()
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck
	raw := jsonrpcCall(t, conn, method, params)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal %s: %v", method, err)
	}
	if envelope.Error == nil {
		t.Fatalf("%s succeeded, want a refusal", method)
	}
	return envelope.Error
}

// TestHostModeReadsTheHostRecordThroughTheProfilesDestination: the editor
// names a connection, the backend resolves it as an open would, and the answer
// is the HOST's recorded method — not a copy on the connection, which no
// longer has one (ADR-727g63tr decision 1).
func TestHostModeReadsTheHostRecordThroughTheProfilesDestination(t *testing.T) {
	modes := newHostModeStore(t)
	if err := modes.Set("SHA256:known", hostmode.Script); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := modes.Set("SHA256:other", hostmode.Raw); err != nil {
		t.Fatalf("Set: %v", err)
	}
	resolver := &fakeHostModeResolver{host: "alias.example", cfg: &ssh.ConnectConfig{Port: 2222}}
	index := &fakeHostKeyIndex{fingerprints: []string{"SHA256:known"}}
	srv := startHostModeServer(t, modes, resolver, index)

	got := hostModeCall(t, srv, "connections.hostMode", map[string]any{"profileId": "ssh-1"})
	if got.ProfileID != "ssh-1" || got.Mode != "script" {
		t.Fatalf("result = %+v, want ssh-1/script", got)
	}
	if len(resolver.asked) != 1 || resolver.asked[0] != "ssh-1" {
		t.Fatalf("resolver asked %v, want the named connection", resolver.asked)
	}
	if len(index.askedHosts) != 1 || index.askedHosts[0] != "alias.example" {
		t.Fatalf("known_hosts asked for %v, want the profile's own host", index.askedHosts)
	}
}

// TestHostModeIsAutoForAHostNobodyAnswered: no record is auto, which is what
// the editor shows for a connection whose key nobody has trusted yet.
func TestHostModeIsAutoForAHostNobodyAnswered(t *testing.T) {
	modes := newHostModeStore(t)
	srv := startHostModeServer(t, modes,
		&fakeHostModeResolver{host: "host.example", cfg: &ssh.ConnectConfig{}},
		&fakeHostKeyIndex{fingerprints: []string{"SHA256:unanswered"}})

	got := hostModeCall(t, srv, "connections.hostMode", map[string]any{"profileId": "ssh-1"})
	if got.Mode != "auto" {
		t.Fatalf("mode = %q, want auto for a host with no record", got.Mode)
	}
}

// TestSetHostModeRecordsEveryKnownKeyOfTheDestination: a host that offers two
// algorithms has two fingerprints, and the dial decides which one it meets.
// An answer written for one of them would be found on the day that key was
// negotiated and lost the next.
func TestSetHostModeRecordsEveryKnownKeyOfTheDestination(t *testing.T) {
	modes := newHostModeStore(t)
	srv := startHostModeServer(t, modes,
		&fakeHostModeResolver{host: "host.example", cfg: &ssh.ConnectConfig{}},
		&fakeHostKeyIndex{fingerprints: []string{"SHA256:one", "SHA256:two"}})

	got := hostModeCall(t, srv, "connections.setHostMode", map[string]any{"profileId": "ssh-1", "mode": "raw"})
	if got.Mode != "raw" || got.Note != "" {
		t.Fatalf("result = %+v, want raw with no note", got)
	}
	for _, fingerprint := range []string{"SHA256:one", "SHA256:two"} {
		if mode, ok := modes.Lookup(fingerprint); !ok || mode != hostmode.Raw {
			t.Errorf("Lookup(%s) = %q/%v, want raw", fingerprint, mode, ok)
		}
	}
}

// TestSetHostModeAutoForgetsTheHostsAnswer: auto is not a value written into
// the record — it is the absence of one (ADR-0033), so choosing it in the
// editor must remove what is there and leave the next open free to ask.
func TestSetHostModeAutoForgetsTheHostsAnswer(t *testing.T) {
	modes := newHostModeStore(t)
	if err := modes.Set("SHA256:one", hostmode.Helper); err != nil {
		t.Fatalf("Set: %v", err)
	}
	srv := startHostModeServer(t, modes,
		&fakeHostModeResolver{host: "host.example", cfg: &ssh.ConnectConfig{}},
		&fakeHostKeyIndex{fingerprints: []string{"SHA256:one"}})

	got := hostModeCall(t, srv, "connections.setHostMode", map[string]any{"profileId": "ssh-1", "mode": "auto"})
	if got.Mode != "auto" {
		t.Fatalf("mode = %q, want auto", got.Mode)
	}
	if mode, ok := modes.Lookup("SHA256:one"); ok {
		t.Fatalf("Lookup after auto = %q/%v, want no record", mode, ok)
	}
}

// TestSetHostModeRefusesAMethodForAHostWithNoKnownKey: a record is keyed by a
// fingerprint, and a destination nobody has trusted has none. Refusing with a
// sentence that says what to do is the only honest answer — the alternative is
// a second place an answer could live, which is what this change removes.
func TestSetHostModeRefusesAMethodForAHostWithNoKnownKey(t *testing.T) {
	modes := newHostModeStore(t)
	srv := startHostModeServer(t, modes,
		&fakeHostModeResolver{host: "host.example", cfg: &ssh.ConnectConfig{}},
		&fakeHostKeyIndex{})

	refusal := hostModeCallError(t, srv, "connections.setHostMode", map[string]any{"profileId": "ssh-1", "mode": "script"})
	if refusal.Code != -32603 {
		t.Errorf("code = %d, want -32603", refusal.Code)
	}
	if refusal.Message != hostModeUnanswered {
		t.Errorf("message = %q, want the sentence that says to connect once", refusal.Message)
	}

	// Auto is the state such a host is already in, so it is not a refusal.
	got := hostModeCall(t, srv, "connections.setHostMode", map[string]any{"profileId": "ssh-1", "mode": "auto"})
	if got.Mode != "auto" {
		t.Fatalf("auto for an unanswered host answered %q", got.Mode)
	}
}

// TestSetHostModeSaysWhenTheRecordCouldNotBeSaved: the method stands for this
// run (hostmode.Store keeps a failed write in memory), so the call succeeds —
// and the sentence saying it will not survive a restart rides the result,
// because a silent failure is a person told nothing about a host that will be
// asked again.
func TestSetHostModeSaysWhenTheRecordCouldNotBeSaved(t *testing.T) {
	modes := hostmode.NewStore(log.NewSlogAdapter(nil), refusingDocumentStore{}, "host-modes.json")
	srv := startHostModeServer(t, modes,
		&fakeHostModeResolver{host: "host.example", cfg: &ssh.ConnectConfig{}},
		&fakeHostKeyIndex{fingerprints: []string{"SHA256:one"}})

	got := hostModeCall(t, srv, "connections.setHostMode", map[string]any{"profileId": "ssh-1", "mode": "script"})
	if got.Mode != "script" {
		t.Fatalf("mode = %q, want script", got.Mode)
	}
	if got.Note != hostModeNotSaved {
		t.Fatalf("note = %q, want the sentence that says it was not saved", got.Note)
	}
	if mode, ok := modes.Lookup("SHA256:one"); !ok || mode != hostmode.Script {
		t.Fatalf("Lookup = %q/%v, want the answer kept for this run", mode, ok)
	}
}

// TestHostModeMethodsRefuseByNamWithoutTheirSeams: both methods need the
// profile resolver and the known_hosts reader. A handler that answered anyway
// would invent a fingerprint, and an answer written under one nobody looks up
// again is a write that silently did nothing.
func TestHostModeMethodsRefuseWithoutTheirSeams(t *testing.T) {
	modes := newHostModeStore(t)

	noResolver := startHostModeServer(t, modes, nil, &fakeHostKeyIndex{})
	if refusal := hostModeCallError(t, noResolver, "connections.hostMode", map[string]any{"profileId": "ssh-1"}); refusal.Code != -32603 {
		t.Errorf("no resolver: code = %d, want -32603", refusal.Code)
	}

	noIndex := startHostModeServer(t, modes, &fakeHostModeResolver{host: "h", cfg: &ssh.ConnectConfig{}}, nil)
	if refusal := hostModeCallError(t, noIndex, "connections.setHostMode", map[string]any{"profileId": "ssh-1", "mode": "raw"}); refusal.Code != -32603 {
		t.Errorf("no index: code = %d, want -32603", refusal.Code)
	}

	noStore := startHostModeServer(t, nil, &fakeHostModeResolver{host: "h", cfg: &ssh.ConnectConfig{}}, &fakeHostKeyIndex{fingerprints: []string{"SHA256:one"}})
	if refusal := hostModeCallError(t, noStore, "connections.hostMode", map[string]any{"profileId": "ssh-1"}); refusal.Code != -32603 {
		t.Errorf("no store: code = %d, want -32603", refusal.Code)
	}
}

// TestHostModeRefusesAnUnknownModeAndAMissingProfile: the closed set is the
// wire's, checked before any store is touched.
func TestHostModeRefusesAnUnknownModeAndAMissingProfile(t *testing.T) {
	modes := newHostModeStore(t)
	srv := startHostModeServer(t, modes,
		&fakeHostModeResolver{host: "h", cfg: &ssh.ConnectConfig{}},
		&fakeHostKeyIndex{fingerprints: []string{"SHA256:one"}})

	if refusal := hostModeCallError(t, srv, "connections.setHostMode", map[string]any{"profileId": "ssh-1", "mode": "ask"}); refusal.Code != -32602 {
		t.Errorf("unknown mode: code = %d, want -32602", refusal.Code)
	}
	if refusal := hostModeCallError(t, srv, "connections.hostMode", map[string]any{}); refusal.Code != -32602 {
		t.Errorf("missing profileId: code = %d, want -32602", refusal.Code)
	}
}

// TestHostModeReadKeepsAutoWhenKnownHostsCannotBeRead: a store this machine
// cannot parse says nothing about the host, and "nothing" is auto — the same
// fail-closed answer the record's own document gives. The dial reports the
// broken store when a connection is actually opened.
func TestHostModeReadKeepsAutoWhenKnownHostsCannotBeRead(t *testing.T) {
	modes := newHostModeStore(t)
	srv := startHostModeServer(t, modes,
		&fakeHostModeResolver{host: "h", cfg: &ssh.ConnectConfig{}},
		&fakeHostKeyIndex{err: errors.New("known_hosts does not parse")})

	got := hostModeCall(t, srv, "connections.hostMode", map[string]any{"profileId": "ssh-1"})
	if got.Mode != "auto" {
		t.Fatalf("mode = %q, want auto", got.Mode)
	}
}

// TestHostModeResult_DTOConformsToContract is the cheap half of AGENTS.md
// rule 5: both result shapes marshal to something their schemas accept.
func TestHostModeResult_DTOConformsToContract(t *testing.T) {
	cases := map[string]struct {
		schema string
		dto    hostModeResult
	}{
		"read, a recorded host":    {schema: "connections.hostMode.schema.json", dto: hostModeResult{ProfileID: "ssh-1", Mode: "script"}},
		"read, an unanswered host": {schema: "connections.hostMode.schema.json", dto: hostModeResult{ProfileID: "ssh-1", Mode: "auto"}},
		"write, saved":             {schema: "connections.setHostMode.schema.json", dto: hostModeResult{ProfileID: "ssh-1", Mode: "raw"}},
		"write, not saved":         {schema: "connections.setHostMode.schema.json", dto: hostModeResult{ProfileID: "ssh-1", Mode: "helper", Note: hostModeNotSaved}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(tc.dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, loadSchema(t, tc.schema), raw, name)
		})
	}
}

// TestHostMode_OverTheWireConformsToContract is the half that matters: the
// real results, off the real socket, satisfy the schemas the renderer's types
// are generated from.
func TestHostMode_OverTheWireConformsToContract(t *testing.T) {
	modes := newHostModeStore(t)
	if err := modes.Set("SHA256:known", hostmode.Script); err != nil {
		t.Fatalf("Set: %v", err)
	}
	srv := startHostModeServer(t, modes,
		&fakeHostModeResolver{host: "host.example", cfg: &ssh.ConnectConfig{}},
		&fakeHostKeyIndex{fingerprints: []string{"SHA256:known"}})
	conn := connectWS(t, srv)
	defer conn.Close() //nolint:errcheck

	readSchema := loadSchema(t, "connections.hostMode.schema.json")
	writeSchema := loadSchema(t, "connections.setHostMode.schema.json")
	for _, call := range []struct {
		method string
		params map[string]any
		schema string
	}{
		{"connections.hostMode", map[string]any{"profileId": "ssh-1"}, "connections.hostMode.schema.json"},
		{"connections.setHostMode", map[string]any{"profileId": "ssh-1", "mode": "raw"}, "connections.setHostMode.schema.json"},
	} {
		raw := jsonrpcCall(t, conn, call.method, call.params)
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("unmarshal %s: %v", call.method, err)
		}
		if len(envelope.Result) == 0 {
			t.Fatalf("%s answered no result: %s", call.method, raw)
		}
		var schema = readSchema
		if call.schema == "connections.setHostMode.schema.json" {
			schema = writeSchema
		}
		validateJSON(t, schema, envelope.Result, call.method+" result off the socket")
	}
}
