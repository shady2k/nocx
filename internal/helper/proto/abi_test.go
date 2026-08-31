package proto

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// These tests guard a FROZEN surface. A generation is content-addressed and
// immutable, two of them are resident at once, and one lingers for as long as
// it holds a session — months. So every assertion here is about something that
// cannot be corrected by a later release: a type that is a bare string today
// can never become an identity, and an opaque identifier can never become
// authorization.

// TestTheThreeIdentitiesAreDefinedTypesNotAliases is D2 stated as a property
// of the type system rather than of a naming convention. `type AttachmentID =
// string` would compile, would pass every round-trip test in this file, and
// would let a session id be handed to a call that wants an attachment — which
// is precisely the conflation that makes a replacing coordinator delete live
// work. A defined type carries its own PkgPath and Name; an alias carries the
// underlying type's, which is what this reads.
func TestTheThreeIdentitiesAreDefinedTypesNotAliases(t *testing.T) {
	const pkg = "github.com/shady2k/nocx/internal/helper/proto"
	cases := []struct {
		name  string
		value any
	}{
		{"GenerationID", GenerationID("")},
		{"HostSessionID", HostSessionID{}},
		{"AttachmentID", AttachmentID("")},
		{"StreamOffset", StreamOffset(0)},
		{"SubscriberID", SubscriberID("")},
		{"LeaseEpoch", LeaseEpoch(0)},
	}
	for _, tc := range cases {
		ty := reflect.TypeOf(tc.value)
		if ty.Name() != tc.name || ty.PkgPath() != pkg {
			t.Errorf("%s is %s.%s — an alias, not a defined type", tc.name, ty.PkgPath(), ty.Name())
		}
	}
}

// TestAHostSessionIsAlwaysQualifiedByItsGeneration is the other half of D2:
// the durable handle names the generation that minted it, and a bare session
// string is not a handle. A struct is what makes the qualification structural —
// there is no way to spell an unqualified one.
func TestAHostSessionIsAlwaysQualifiedByItsGeneration(t *testing.T) {
	ty := reflect.TypeOf(HostSessionID{})
	if ty.Kind() != reflect.Struct {
		t.Fatalf("HostSessionID is %s, want a struct so a session can never be spelled without its generation", ty.Kind())
	}
	gen, ok := ty.FieldByName("Generation")
	if !ok || gen.Type != reflect.TypeOf(GenerationID("")) {
		t.Fatalf("HostSessionID.Generation is missing or not a GenerationID")
	}
	if _, ok := ty.FieldByName("Session"); !ok {
		t.Fatal("HostSessionID.Session is missing")
	}
	raw, err := json.Marshal(HostSessionID{Generation: "g", Session: "s"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(raw), `{"generation":"g","session":"s"}`; got != want {
		t.Fatalf("HostSessionID marshals to %s, want %s", got, want)
	}
}

// TestNoCallConflatesTheIdentities walks every envelope of the frozen ABI and
// checks the declared Go type of each field. This is the assertion the bead's
// acceptance criterion spells "no call conflates them": a future edit that
// widens AttachParams.Session to a string, or keys an ack by an attachment
// instead of a subscriber, fails here rather than in a generation nobody can
// change any more.
func TestNoCallConflatesTheIdentities(t *testing.T) {
	sessionID := reflect.TypeOf(HostSessionID{})
	subscriber := reflect.TypeOf(SubscriberID(""))
	attachment := reflect.TypeOf(AttachmentID(""))
	offset := reflect.TypeOf(StreamOffset(0))
	epoch := reflect.TypeOf(LeaseEpoch(0))

	cases := []struct {
		envelope any
		field    string
		want     reflect.Type
	}{
		{AttachParams{}, "Subscriber", subscriber},
		{AttachParams{}, "Session", sessionID},
		{AttachParams{}, "Offset", offset},
		{AttachResult{}, "Attachment", attachment},
		{AckParams{}, "Subscriber", subscriber},
		{AckParams{}, "Session", sessionID},
		{AckParams{}, "Offset", offset},
		{DetachParams{}, "Attachment", attachment},
		{Resume{}, "From", offset},
		{WriteGrant{}, "Epoch", epoch},
		{WriteGrant{}, "Holder", reflect.TypeOf((*SubscriberID)(nil))},
		{SessionReset{}, "Subscriber", subscriber},
		{SessionReset{}, "Session", sessionID},
	}
	for _, tc := range cases {
		ty := reflect.TypeOf(tc.envelope)
		f, ok := ty.FieldByName(tc.field)
		if !ok {
			t.Errorf("%s has no field %s", ty.Name(), tc.field)
			continue
		}
		if f.Type != tc.want {
			t.Errorf("%s.%s is %s, want %s", ty.Name(), tc.field, f.Type, tc.want)
		}
	}
}

// TestTheReadCursorIsKeyedBySubscriberNotAttachment is D2's "streamOffset
// survives attachments" made checkable. An ack keyed by the attachment would
// restart every reader's cursor when its connection was replaced, which is the
// same defect as treating a new attachment as a new stream.
func TestTheReadCursorIsKeyedBySubscriberNotAttachment(t *testing.T) {
	ty := reflect.TypeOf(AckParams{})
	for i := 0; i < ty.NumField(); i++ {
		if ty.Field(i).Type == reflect.TypeOf(AttachmentID("")) {
			t.Fatalf("AckParams.%s is an AttachmentID: the read cursor belongs to the subscriber and outlives the attachment (D2)", ty.Field(i).Name)
		}
	}
}

// TestStreamOffsetIsSixtyFourBit — D15 reserves "session-keyed, 64-bit
// offsets", and a 32-bit cursor would wrap after 4 GiB of output, which a
// three-hour build reaches. The value below is above MaxUint32 and must
// survive the wire whole.
func TestStreamOffsetIsSixtyFourBit(t *testing.T) {
	const beyond32 = StreamOffset(math.MaxUint32) + 1
	raw, err := json.Marshal(Resume{Resumed: true, From: beyond32})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Resume
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.From != beyond32 {
		t.Fatalf("offset %d round-tripped as %d", beyond32, back.From)
	}
}

// TestFreshIsCarriedExplicitlyAndNeverInferred — D5 of the generation-daemon
// draft: "a fresh renderer can attach at a non-zero offset; a renderer can hold
// an offset after losing its screen. Only the caller knows." An omitempty on
// this field would make `false` and `absent` the same bytes, and the helper
// would be back to inferring it from the offset.
func TestFreshIsCarriedExplicitlyAndNeverInferred(t *testing.T) {
	raw, err := json.Marshal(AttachParams{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"fresh":false`) {
		t.Fatalf("attach params %s omit fresh when false — the flag must be explicit", raw)
	}
	if !strings.Contains(string(raw), `"requestWrite":false`) {
		t.Fatalf("attach params %s omit requestWrite when false — the write request must be explicit", raw)
	}
}

// TestNoEnvelopeReservesACapability is D12 enforced rather than merely
// written. The owner decided on 2026-08-31 that same-UID trust is the boundary
// and that no session capability is reserved. The consequence is that this
// cannot be retrofitted: a generation deployed without one accepts any
// same-UID peer for the whole of its life, so if that decision is ever
// reversed the field is owed BEFORE the next generation ships, and reversing
// it starts by changing this test and reading why it was here.
func TestNoEnvelopeReservesACapability(t *testing.T) {
	forbidden := []string{"token", "secret", "credential", "auth", "capability", "nonce"}
	envelopes := []any{AttachParams{}, AttachResult{}, AckParams{}, DetachParams{}, DetachResult{}, Resume{}, WriteGrant{}, SessionReset{}}
	for _, e := range envelopes {
		ty := reflect.TypeOf(e)
		for i := 0; i < ty.NumField(); i++ {
			name := strings.ToLower(ty.Field(i).Tag.Get("json"))
			for _, bad := range forbidden {
				if strings.Contains(name, bad) {
					t.Errorf("%s.%s reserves %q: D12 reserves no session capability, and an opaque identifier can never later become authorization", ty.Name(), ty.Field(i).Name, name)
				}
			}
		}
	}
}

// TestTheLedgerCannotNameAnAttachment is D2's "attachmentId appears NOWHERE in
// the ledger", checked structurally: internal/content does not depend on this
// package, so it cannot hold one of these types. It does not prove nobody ever
// persists the string — nothing can — but the identities are defined types, so
// putting one in a row needs a conversion, and the conversion needs this
// import.
func TestTheLedgerCannotNameAnAttachment(t *testing.T) {
	const self = "github.com/shady2k/nocx/internal/helper/proto"
	dir := filepath.Join("..", "..", "content")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read internal/content: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == self {
				t.Errorf("internal/content/%s imports the helper ABI: an attachment id is disposable and may not reach the ledger (D2)", e.Name())
			}
		}
	}
}

// TestSessionDataFrameLayoutIsFrozen pins the binary layout with LITERAL
// bytes rather than with an encoder's own output — a golden vector built by
// the codec under test proves the codec agrees with itself and nothing else.
// The layout is the coordinator's own data frame extended by exactly what D8
// adds, and no more: a subscriber, because a stream now has several readers,
// and a lease epoch, because exactly one of them may write.
func TestSessionDataFrameLayoutIsFrozen(t *testing.T) {
	payload := []byte{
		// session-id, 16 raw bytes
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		// subscriber-id, 16 raw bytes
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
		// lease epoch, uint64 big-endian
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x2c,
		// the PTY bytes, raw and never encoded
		'h', 'i',
	}
	if len(payload)-2 != SessionFrameHeaderLen {
		t.Fatalf("golden header is %d bytes, SessionFrameHeaderLen is %d", len(payload)-2, SessionFrameHeaderLen)
	}
	f, err := DecodeSessionFrame(payload)
	if err != nil {
		t.Fatalf("DecodeSessionFrame: %v", err)
	}
	if f.Session != [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10} {
		t.Errorf("session = %x", f.Session)
	}
	if f.Subscriber != [16]byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20} {
		t.Errorf("subscriber = %x", f.Subscriber)
	}
	if f.Epoch != 300 {
		t.Errorf("epoch = %d, want 300", f.Epoch)
	}
	if string(f.Payload) != "hi" {
		t.Errorf("payload = %q, want %q", f.Payload, "hi")
	}
}

// TestSessionDataFrameCarriesNoOffset is the reuse decision, asserted so it is
// not quietly undone. AD-9's coordinate is already carried the way
// internal/transport carries it — "the client counts received payload bytes
// per session; the binary frame header carries no offset field" — and each
// subscriber counts from the `from` its attach answered. An offset per frame
// would be a second, disagreeing statement of the same position.
func TestSessionDataFrameCarriesNoOffset(t *testing.T) {
	ty := reflect.TypeOf(SessionFrame{})
	for i := 0; i < ty.NumField(); i++ {
		if ty.Field(i).Type == reflect.TypeOf(StreamOffset(0)) {
			t.Fatalf("SessionFrame.%s carries an offset: the reader counts bytes from its attach, as AD-9 already has it", ty.Field(i).Name)
		}
	}
}

// TestAShortSessionFrameIsRefusedRatherThanPanicking — the same rule
// internal/transport states for its own data frame: a frame shorter than its
// header is logged and dropped, never a panic and never a torn-down
// connection.
func TestAShortSessionFrameIsRefusedRatherThanPanicking(t *testing.T) {
	for _, n := range []int{0, 1, SessionFrameHeaderLen - 1} {
		if _, err := DecodeSessionFrame(make([]byte, n)); err == nil {
			t.Errorf("a %d-byte session frame decoded without error", n)
		}
	}
	// Exactly the header and no payload is legitimate: a zero-length write is
	// not a malformed frame.
	f, err := DecodeSessionFrame(make([]byte, SessionFrameHeaderLen))
	if err != nil {
		t.Fatalf("header-only frame: %v", err)
	}
	if len(f.Payload) != 0 {
		t.Fatalf("header-only frame has %d payload bytes", len(f.Payload))
	}
}

// TestTheSessionFrameTypeIsInTheClosedSet is why the type byte is allocated
// NOW and not when the session service lands. The decoder treats an unknown
// type byte as garbage and scans past it one byte at a time; a generation that
// did not know this type would resync through a live PTY stream instead of
// dropping one frame. This is the same forward-compatibility move AD-1 made
// when it allocated the metadata msg-type before the helper existed.
func TestTheSessionFrameTypeIsInTheClosedSet(t *testing.T) {
	body := make([]byte, SessionFrameHeaderLen+3)
	copy(body[SessionFrameHeaderLen:], "abc")
	wire := EncodeFrame(TypeSessionData, 0, 0, body)

	var gotType FrameType
	var gotPayload []byte
	garbage := 0
	dec := NewDecoder(func(ty FrameType, _, _ uint32, payload []byte) {
		gotType, gotPayload = ty, payload
	}, func(n int) { garbage += n })
	if err := dec.Feed(wire); err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if garbage != 0 {
		t.Fatalf("%d bytes of a session data frame were scanned past as garbage", garbage)
	}
	if gotType != TypeSessionData || len(gotPayload) != len(body) {
		t.Fatalf("frame delivered as type %d with %d bytes", gotType, len(gotPayload))
	}
}
