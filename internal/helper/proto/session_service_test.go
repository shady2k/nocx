package proto

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The frozen half of nocx-k6p18.1 said what an attachment is. This half says
// what it attaches TO: a session the helper spawned, and the inventory of the
// sessions it holds. Both envelopes carry D15's opaque WorkspaceID, and this
// is the last moment either can be decided — nothing is deployed yet.

// TestSpawnAndSessionsCarryTheReservedWorkspace is D15's reservation made
// enforceable rather than left in a doc comment. The workspace is
// coordinator-owned today and `workspace.Default` is a coordinator-side
// constant, so a later optimisation looking only at what is READ would find
// both fields unused and remove them — after which one workspace can never be
// reachable from two machines without a wire break.
func TestSpawnAndSessionsCarryTheReservedWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"SpawnParams", reflect.TypeOf(SpawnParams{})},
		{"SessionsParams", reflect.TypeOf(SessionsParams{})},
		{"SessionEntry", reflect.TypeOf(SessionEntry{})},
	} {
		f, ok := tc.typ.FieldByName("Workspace")
		if !ok {
			t.Fatalf("%s has no Workspace field: D15 reserves it in spawn, in the inventory and in every entry", tc.name)
		}
		if f.Type != reflect.TypeOf(WorkspaceID("")) {
			t.Errorf("%s.Workspace is %s, want WorkspaceID — a defined type so it cannot be spelled as a display name", tc.name, f.Type)
		}
	}
}

// TestTheHelperPersistsNoHumanAuthoredName is D3 as a compile-time-ish fact.
// The helper may report DERIVED diagnostics because the OS is their source; it
// may not persist a name a person typed. A `name`, `title`, `label` or `alias`
// field on the inventory entry is the exact shape that decision refuses, and
// it is easier to add one by accident than to notice it later — a friendly
// alias is a local projection owned by the local server. One owner ever.
func TestTheHelperPersistsNoHumanAuthoredName(t *testing.T) {
	forbidden := []string{"name", "title", "label", "alias", "displayname", "friendlyname"}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(SessionEntry{}),
		reflect.TypeOf(SpawnParams{}),
		reflect.TypeOf(LaunchRecord{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			got := strings.ToLower(typ.Field(i).Name)
			for _, bad := range forbidden {
				if got == bad {
					t.Errorf("%s.%s: the helper owns no human-authored name (D3)", typ, typ.Field(i).Name)
				}
			}
		}
	}
}

// TestLaunchIsAuthorityAndObservationIsEvidence is the single most likely
// design error in this bead, asserted so it cannot be made quietly. argv is
// mutable, a process can be replaced, and /proc's semantics differ per OS — so
// what the helper RECORDED when it spawned and what it later READ off the OS
// are two different facts and may never be one field. An entry that merged
// them would report a lie with the authority of a launch record.
func TestLaunchIsAuthorityAndObservationIsEvidence(t *testing.T) {
	entry := reflect.TypeOf(SessionEntry{})
	launch, ok := entry.FieldByName("Launch")
	if !ok {
		t.Fatal("SessionEntry has no Launch: the helper's own record of what it spawned is the canonical identity (D10)")
	}
	if launch.Type != reflect.TypeOf(LaunchRecord{}) {
		t.Errorf("SessionEntry.Launch is %s, want LaunchRecord", launch.Type)
	}
	observed, ok := entry.FieldByName("Observed")
	if !ok {
		t.Fatal("SessionEntry has no Observed: OS inspection is a fallback and a cross-check (D10)")
	}
	if observed.Type != reflect.TypeOf((*Observation)(nil)) {
		t.Errorf("SessionEntry.Observed is %s, want *Observation — absent when the OS could not be asked, never an empty record passed off as an answer", observed.Type)
	}
	// And the evidence says where it came from, because evidence with no
	// provenance cannot be weighed against the record it contradicts.
	if _, ok := reflect.TypeOf(Observation{}).FieldByName("Source"); !ok {
		t.Error("Observation has no Source: evidence that cannot say where it came from cannot be weighed")
	}
}

// TestAbsenceIsSaidExplicitlyRatherThanOmitted pairs with the type check
// above. A nil Observation marshals to `null` and never to `{}` — an empty
// record decodes as "we looked and the process has no cwd" while the truth is
// "this OS was not asked" — and never to an OMITTED field either, because
// absent and null are different bytes: a reader must be able to tell "no
// observation" from "this generation does not send observations". The same
// holds for the writer and the exit status, and vault.status's missing
// defaultProvider is what leaving that implicit cost a release.
func TestAbsenceIsSaidExplicitlyRatherThanOmitted(t *testing.T) {
	raw, err := json.Marshal(SessionEntry{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{`"observed":null`, `"writer":null`, `"exit":null`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("an empty entry does not say %s: %s", field, raw)
		}
	}
}

// TestSpawnTakesNoArgv is the params half of the rule host.Register enforces
// at registration (D3). It is asserted here as well as there because this is
// where the shape is FROZEN: a generation that shipped a spawn taking argv
// keeps taking it for the life of its sessions.
func TestSpawnTakesNoArgv(t *testing.T) {
	typ := reflect.TypeOf(SpawnParams{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.String {
			t.Errorf("SpawnParams.%s is a free-form string list: no operation may accept argv (D3)", f.Name)
		}
	}
}

// TestTheWindowBoundTravelsAtSpawn is D8's "the coordinator decides, the helper
// applies", and the consequence it names: a generation keeps the bound it was
// given for the life of its sessions, so changing the setting affects the next
// session and never a running one. A bound that arrived any other way — a
// helper-side setting, an environment variable — would be a second owner of a
// per-destination product decision.
func TestTheWindowBoundTravelsAtSpawn(t *testing.T) {
	f, ok := reflect.TypeOf(SpawnParams{}).FieldByName("WindowBytes")
	if !ok {
		t.Fatal("SpawnParams has no WindowBytes: the bound travels at spawn (D8)")
	}
	if f.Type.Kind() != reflect.Int64 {
		t.Errorf("SpawnParams.WindowBytes is %s: it is int64 so a conversion to int cannot overflow on a 32-bit host, which D8 asks for by name", f.Type)
	}
}

// TestResumeAtServesAnOffsetTheWindowStillHolds is the ordinary half of the
// decision rule, and it is the paired positive for every reset case below:
// on a window that still holds the requested offset, the reader resumes there
// and loses nothing.
func TestResumeAtServesAnOffsetTheWindowStillHolds(t *testing.T) {
	r := ResumeAt(100, 500, 300)
	if !r.Resumed || r.Reset {
		t.Fatalf("resumed=%v reset=%v, want an in-window offset served", r.Resumed, r.Reset)
	}
	if r.From != 300 {
		t.Errorf("from = %d, want the requested offset", r.From)
	}
	if r.Gap != nil {
		t.Errorf("gap = %+v, want none: nothing was lost", r.Gap)
	}
	// The base itself is in the window — the boundary is inclusive, and a
	// reader told to restart at the base must not be reset again when it does.
	if r := ResumeAt(100, 500, 100); !r.Resumed || r.Reset {
		t.Errorf("the window's own base was reset: %+v", r)
	}
}

// TestResumeAtResetsToTheBaseAndNamesWhatWasLost is the decision rule
// nocx-k6p18.1 deliberately did not write, so that the window and the rule
// would land together with one owner. A reset that does not say what is
// missing is the silent degrade AGENTS.md forbids: the reader goes on
// rendering as though the stream were continuous.
func TestResumeAtResetsToTheBaseAndNamesWhatWasLost(t *testing.T) {
	r := ResumeAt(1000, 5000, 40)
	if r.Resumed || !r.Reset {
		t.Fatalf("resumed=%v reset=%v, want a reclaimed offset reset", r.Resumed, r.Reset)
	}
	if r.From != 1000 {
		t.Errorf("from = %d, want the window's base: the bytes that still exist are the honest restart", r.From)
	}
	if r.Gap == nil {
		t.Fatal("a reset with no gap: the loss must be stated")
	}
	if r.Gap.Start != 40 || r.Gap.End != 1000 {
		t.Errorf("gap = [%d,%d), want [40,1000) — from where the reader stood to where the stream still is", r.Gap.Start, r.Gap.End)
	}
	if r.Gap.Reason != GapReasonWindow {
		t.Errorf("reason = %q, want %q: nobody ever held these bytes, so it is not the recording's cap", r.Gap.Reason, GapReasonWindow)
	}
}

// TestResumeAtAndResetAreMutuallyExclusive pins the invariant the contract
// states — exactly one of them is true — across the boundaries, so a reader
// can decode "the helper said no reset" and never meet "the helper said both".
func TestResumeAtAndResetAreMutuallyExclusive(t *testing.T) {
	for _, tc := range []struct{ base, written, req StreamOffset }{
		{0, 0, 0}, {0, 10, 0}, {0, 10, 10}, {0, 10, 99}, {10, 10, 9}, {10, 20, 10}, {10, 20, 19},
	} {
		r := ResumeAt(tc.base, tc.written, tc.req)
		if r.Resumed == r.Reset {
			t.Errorf("ResumeAt(%d,%d,%d) = resumed:%v reset:%v, want exactly one", tc.base, tc.written, tc.req, r.Resumed, r.Reset)
		}
		if (r.Gap != nil) != r.Reset {
			t.Errorf("ResumeAt(%d,%d,%d): gap present=%v, reset=%v — a gap rides exactly with a reset", tc.base, tc.written, tc.req, r.Gap != nil, r.Reset)
		}
	}
}

// TestAReaderAheadOfTheStreamIsNotReset is the failure path on the other side
// of the window. A cursor past what was produced is a caller defect, not a
// reclaimed range, and answering it with a reset would tell the reader bytes
// were lost that were never produced — a false statement in the product.
func TestAReaderAheadOfTheStreamIsNotReset(t *testing.T) {
	r := ResumeAt(100, 500, 900)
	if r.Reset {
		t.Fatalf("a cursor ahead of the stream was reset: %+v", r)
	}
	if r.From != 500 {
		t.Errorf("from = %d, want the stream's end: the reader waits there for bytes that do not exist yet", r.From)
	}
}

// TestEncodedSessionFrameMatchesTheFrozenGoldenVector is note 2 of the bead:
// the encode half lands with its producer, and it is checked against the
// literal bytes committed in abi_test.go rather than against its own decoder.
// A vector built by the codec under test proves only that the codec agrees
// with itself.
func TestEncodedSessionFrameMatchesTheFrozenGoldenVector(t *testing.T) {
	golden := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x2c,
		'h', 'i',
	}
	f := SessionFrame{
		Session:    [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		Subscriber: [16]byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20},
		Epoch:      300,
		Payload:    []byte("hi"),
	}
	got := EncodeSessionFrame(f)
	if string(got) != string(golden) {
		t.Fatalf("encoded\n %x\nwant\n %x", got, golden)
	}
}

// TestAZeroLengthWriteEncodesAsAHeaderOnlyFrame — the encoder's counterpart of
// the decoder's rule. A write of no bytes is legitimate and must not become a
// frame the decoder refuses.
func TestAZeroLengthWriteEncodesAsAHeaderOnlyFrame(t *testing.T) {
	got := EncodeSessionFrame(SessionFrame{Epoch: 1})
	if len(got) != SessionFrameHeaderLen {
		t.Fatalf("encoded %d bytes, want exactly the header", len(got))
	}
	back, err := DecodeSessionFrame(got)
	if err != nil {
		t.Fatalf("the encoder produced a frame its own decoder refuses: %v", err)
	}
	if back.Epoch != 1 || len(back.Payload) != 0 {
		t.Fatalf("round trip = %+v", back)
	}
}

// TestSessionIDBytesRoundTripThroughTheirHexSpelling closes the seam between
// the two spellings of one identity: the ABI's 32 hex characters and the data
// frame's 16 raw bytes. They are the same id, and a session addressed by the
// control plane must be reachable on the data plane without a lookup.
func TestSessionIDBytesRoundTripThroughTheirHexSpelling(t *testing.T) {
	raw := [16]byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	hex := SessionHex(raw)
	if len(hex) != 32 {
		t.Fatalf("hex = %q, want 32 characters", hex)
	}
	back, err := SessionBytes(hex)
	if err != nil {
		t.Fatalf("SessionBytes: %v", err)
	}
	if back != raw {
		t.Fatalf("round trip = %x, want %x", back, raw)
	}
	if _, err := SessionBytes("nonsense"); err == nil {
		t.Error("a session id that is not 32 hex characters was accepted")
	}
}
