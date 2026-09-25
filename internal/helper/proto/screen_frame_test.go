package proto

import (
	"bytes"
	"errors"
	"testing"
)

// The SCREEN data plane's own tests. The layout is pinned by literal-byte
// golden vectors rather than by the encoder's own output, for the reason
// session_frame.go gives: a vector built by the codec under test proves only
// that the codec agrees with itself.

// TestScreenFrameGoldenVectors pins the layout in literal bytes: session id,
// subscriber id, revision, part index, part count, payload — in that order,
// big-endian where a field is wider than a byte. A field reordered or a
// width changed moves these bytes, and this test fails.
func TestScreenFrameGoldenVectors(t *testing.T) {
	f := ScreenDataFrame{
		Session:    newID(0xa0),
		Subscriber: newID(0xb0),
		Revision:   7,
		PartIndex:  0,
		PartCount:  1,
		Payload:    []byte("hi"),
	}
	want := append([]byte{
		0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7,
		0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf,
		0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7,
		0xb8, 0xb9, 0xba, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x07,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x01,
	}, 'h', 'i')
	if got := EncodeScreenDataFrame(f); !bytes.Equal(got, want) {
		t.Fatalf("EncodeScreenDataFrame wrote % x, want the literal vector % x", got, want)
	}
	dec, err := DecodeScreenDataFrame(want)
	if err != nil {
		t.Fatalf("DecodeScreenDataFrame: %v", err)
	}
	if dec.Session != f.Session || dec.Subscriber != f.Subscriber || dec.Revision != 7 {
		t.Fatalf("decode read session/subscriber/revision as %x/%x/%d", dec.Session, dec.Subscriber, dec.Revision)
	}
	if dec.PartIndex != 0 || dec.PartCount != 1 || !dec.Whole() || !bytes.Equal(dec.Payload, []byte("hi")) {
		t.Fatalf("decode read parts %d/%d payload %q", dec.PartIndex, dec.PartCount, dec.Payload)
	}
}

func newID(prefix byte) [16]byte {
	var id [16]byte
	for i := range id {
		id[i] = prefix + byte(i)
	}
	return id
}

// TestAShortScreenFrameIsRefusedRatherThanPanicking is the session codec's
// rule carried over: a payload that cannot hold the header is an answer to
// the sender, never a panic, and a payload exactly the header length decodes
// with no bytes.
func TestAShortScreenFrameIsRefusedRatherThanPanicking(t *testing.T) {
	for n := 0; n < ScreenDataFrameHeaderLen; n++ {
		if _, err := DecodeScreenDataFrame(make([]byte, n)); !errors.Is(err, ErrScreenDataFrameTooShort) {
			t.Fatalf("DecodeScreenDataFrame(%d bytes) = %v, want ErrScreenDataFrameTooShort", n, err)
		}
	}
	f, err := DecodeScreenDataFrame(make([]byte, ScreenDataFrameHeaderLen))
	if err != nil {
		t.Fatalf("DecodeScreenDataFrame(header only): %v", err)
	}
	if len(f.Payload) != 0 {
		t.Fatalf("a header-only frame carries %d payload bytes, want none", len(f.Payload))
	}
}

// TestTheScreenFrameTypeIsInTheClosedSet is why the type byte is allocated
// before any producer exists: the decoder must RECOGNISE the screen plane as
// a frame and deliver it, not treat an unknown byte as garbage and resync
// through a live screen stream one byte at a time.
func TestTheScreenFrameTypeIsInTheClosedSet(t *testing.T) {
	if !TypeScreenFrame.valid() {
		t.Fatalf("TypeScreenFrame is not in the closed set")
	}
	body := make([]byte, ScreenDataFrameHeaderLen+3)
	copy(body[ScreenDataFrameHeaderLen:], "abc")
	var delivered int
	d := NewDecoder(func(ft FrameType, seq, ack uint32, payload []byte) {
		delivered++
		if ft != TypeScreenFrame {
			t.Fatalf("decoder delivered type %d, want TypeScreenFrame", ft)
		}
	}, func(int) {
		t.Fatalf("the decoder scanned a live screen frame as garbage")
	})
	if err := d.Feed(EncodeFrame(TypeScreenFrame, 1, 0, body)); err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if delivered != 1 {
		t.Fatalf("decoder delivered %d frames, want 1", delivered)
	}
}

// TestSplitScreenFrameReassemblesToTheOriginalBytes is the whole point of the
// continuation: a payload too big for one wire frame leaves the sender as
// parts, each part within the carrier's bound, and arrives as exactly the
// bytes that entered. A small payload is one part, and says so.
func TestSplitScreenFrameReassemblesToTheOriginalBytes(t *testing.T) {
	session, subscriber := newID(0xa0), newID(0xb0)
	const revision = 42

	small, err := SplitScreenDataFrame(session, subscriber, revision, []byte("tiny"))
	if err != nil {
		t.Fatalf("split a small payload: %v", err)
	}
	if len(small) != 1 || !small[0].Whole() {
		t.Fatalf("a payload within the bound split into %d parts", len(small))
	}

	// Two and a half parts' worth: the split must not pad, and the last part
	// carries the remainder.
	payload := make([]byte, 2*MaxScreenDataPayloadBytes+1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	parts, err := SplitScreenDataFrame(session, subscriber, revision, payload)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("payload of %d bytes split into %d parts, want 3", len(payload), len(parts))
	}
	a := NewScreenAssembler()
	for i, p := range parts {
		if int(p.PartIndex) != i || int(p.PartCount) != len(parts) {
			t.Fatalf("part %d carries index %d of %d", i, p.PartIndex, p.PartCount)
		}
		if got := EncodeScreenDataFrame(p); len(got) > MaxFrameBytes {
			t.Fatalf("part %d encodes to %d bytes, above the wire's own bound", i, len(got))
		}
		dec, err := DecodeScreenDataFrame(EncodeScreenDataFrame(p))
		if err != nil {
			t.Fatalf("decode part %d: %v", i, err)
		}
		assembled, err := a.Feed(dec)
		if err != nil {
			t.Fatalf("feed part %d: %v", i, err)
		}
		if i < len(parts)-1 && assembled != nil {
			t.Fatalf("part %d completed an assembly early", i)
		}
		if i == len(parts)-1 {
			if assembled == nil {
				t.Fatalf("the last part left the assembly incomplete")
			}
			if assembled.Revision != revision || assembled.Key.Session != session || assembled.Key.Subscriber != subscriber {
				t.Fatalf("assembled frame carries key/revision %x/%x/%d", assembled.Key.Session, assembled.Key.Subscriber, assembled.Revision)
			}
			if !bytes.Equal(assembled.Payload, payload) {
				t.Fatalf("reassembled payload is %d bytes, want the original %d intact", len(assembled.Payload), len(payload))
			}
		}
	}
}

// TestAnAssemblerRefusesAnUnassemblablePartCount is the bound named before
// anything is buffered: a part that declares more parts than the carrier
// will ever assemble is dropped at first contact, and nothing grows.
func TestAnAssemblerRefusesAnUnassemblablePartCount(t *testing.T) {
	a := NewScreenAssembler()
	f := ScreenDataFrame{
		Session: newID(0xa0), Subscriber: newID(0xb0),
		Revision: 1, PartIndex: 0, PartCount: MaxScreenAssemblyParts + 1,
		Payload: []byte("x"),
	}
	if _, err := a.Feed(f); !errors.Is(err, ErrScreenAssemblyPartCount) {
		t.Fatalf("Feed = %v, want ErrScreenAssemblyPartCount", err)
	}
	if a.Pending() != 0 {
		t.Fatalf("a refused part count left %d assemblies buffered", a.Pending())
	}
}

// TestPartsOfTwoRevisionsNeverSplice: half of revision N and half of N+1 is
// a screen that never existed. A part arriving against a partial of another
// revision drops the partial WHOLE and refuses the part — never a splice —
// and the new revision's part 0, arriving next, starts clean. A part that
// continues no assembly at all is an orphan and is dropped by name.
func TestPartsOfTwoRevisionsNeverSplice(t *testing.T) {
	session, subscriber := newID(0xa0), newID(0xb0)
	a := NewScreenAssembler()

	part := func(rev uint64, idx, count uint32, body string) ScreenDataFrame {
		return ScreenDataFrame{Session: session, Subscriber: subscriber, Revision: rev, PartIndex: idx, PartCount: count, Payload: []byte(body)}
	}

	// Revision 1, part 0 of 2 — then revision 2 arrives mid-assembly.
	if _, err := a.Feed(part(1, 0, 2, "half of one")); err != nil {
		t.Fatalf("feed revision 1 part 0: %v", err)
	}
	if _, err := a.Feed(part(2, 0, 1, "whole of two")); !errors.Is(err, ErrScreenAssemblySuperseded) {
		t.Fatalf("the mid-assembly revision switch answered %v, want ErrScreenAssemblySuperseded", err)
	}
	// The partial is gone whole, and revision 2 starts clean at its own
	// part 0: nothing of revision 1 can survive into what it assembles.
	if a.Pending() != 0 {
		t.Fatalf("a superseded assembly left %d partials buffered", a.Pending())
	}
	assembled, err := a.Feed(part(2, 0, 1, "whole of two"))
	if err != nil {
		t.Fatalf("revision 2 must assemble clean after the drop: %v", err)
	}
	if assembled == nil || string(assembled.Payload) != "whole of two" {
		t.Fatalf("revision 2 assembled %v", assembled)
	}

	// An orphan: a part with no assembly to continue is dropped by name.
	if _, err := a.Feed(part(3, 1, 3, "orphan")); !errors.Is(err, ErrScreenAssemblyOrphanPart) {
		t.Fatalf("an orphan part answered %v, want ErrScreenAssemblyOrphanPart", err)
	}
	if a.Pending() != 0 {
		t.Fatalf("an orphan part left %d partials buffered", a.Pending())
	}
}

// TestAssembliesAreBoundedPerConnection: a sender that opens assemblies for
// sessions and subscribers nobody reads must not grow the receiver without
// limit. The connection bound refuses a new assembly by name; Abandon frees
// the slot and the bytes, which is what a half-assembled frame that is given
// up owes the process.
func TestAssembliesAreBoundedPerConnection(t *testing.T) {
	a := NewScreenAssembler()
	open := func(k int) error {
		_, err := a.Feed(ScreenDataFrame{
			Session: newID(byte(k)), Subscriber: newID(0xb0),
			Revision: 1, PartIndex: 0, PartCount: 2, Payload: []byte("part"),
		})
		return err
	}
	for k := 0; k < MaxScreenAssemblies; k++ {
		if err := open(k); err != nil {
			t.Fatalf("opening assembly %d: %v", k, err)
		}
	}
	if err := open(MaxScreenAssemblies); !errors.Is(err, ErrScreenAssemblyConnections) {
		t.Fatalf("the bound answered %v, want ErrScreenAssemblyConnections", err)
	}
	key := ScreenKey{Session: newID(3), Subscriber: newID(0xb0)}
	a.Abandon(key)
	if a.Pending() != MaxScreenAssemblies-1 {
		t.Fatalf("after abandoning one assembly, %d remain, want %d", a.Pending(), MaxScreenAssemblies-1)
	}
	// The freed slot takes a new assembly, and the abandoned key never
	// delivers: its bytes went with it.
	if err := open(MaxScreenAssemblies); err != nil {
		t.Fatalf("the freed slot refused a new assembly: %v", err)
	}
	if _, err := a.Feed(ScreenDataFrame{
		Session: key.Session, Subscriber: key.Subscriber,
		Revision: 1, PartIndex: 1, PartCount: 2, Payload: []byte("tail"),
	}); !errors.Is(err, ErrScreenAssemblyOrphanPart) {
		t.Fatalf("feeding an abandoned assembly answered %v, want ErrScreenAssemblyOrphanPart", err)
	}
}

// TestAHandBuiltPartAboveTheBoundIsRefused: the wire cannot produce a part
// above the per-part bound, and the assembler must not accept one from a
// caller's hands either — the ceiling is part count times the bound, and
// every part is checked against it, not only the declared count.
func TestAHandBuiltPartAboveTheBoundIsRefused(t *testing.T) {
	a := NewScreenAssembler()
	f := ScreenDataFrame{
		Session: newID(0xa0), Subscriber: newID(0xb0),
		Revision: 1, PartIndex: 0, PartCount: 1,
		Payload: make([]byte, MaxScreenDataPayloadBytes+1),
	}
	if _, err := a.Feed(f); !errors.Is(err, ErrScreenAssemblyPartTooLarge) {
		t.Fatalf("Feed = %v, want ErrScreenAssemblyPartTooLarge", err)
	}
	if a.Pending() != 0 {
		t.Fatalf("an oversized part left %d assemblies buffered", a.Pending())
	}
}

// TestASplitOnThePartCountBoundaryIsRefusedBeyondIt pins the sender half of
// the assembly bound: a document needing exactly MaxScreenAssemblyParts
// parts splits; one byte more is refused with the receiver's own named
// error and no parts at all — the receiver would refuse the assembly before
// buffering a byte, so the sender must not produce it.
func TestASplitOnThePartCountBoundaryIsRefusedBeyondIt(t *testing.T) {
	session, subscriber := newID(0xa0), newID(0xb0)
	fits := MaxScreenAssemblyParts * MaxScreenDataPayloadBytes
	parts, err := SplitScreenDataFrame(session, subscriber, 1, make([]byte, fits))
	if err != nil {
		t.Fatalf("a document needing exactly the bound split: %v", err)
	}
	if len(parts) != MaxScreenAssemblyParts {
		t.Fatalf("the boundary document split into %d parts, want %d", len(parts), MaxScreenAssemblyParts)
	}
	parts, err = SplitScreenDataFrame(session, subscriber, 1, make([]byte, fits+1))
	if !errors.Is(err, ErrScreenAssemblyPartCount) {
		t.Fatalf("one byte past the bound answered %v, want ErrScreenAssemblyPartCount", err)
	}
	if parts != nil {
		t.Fatalf("a refused split returned %d parts, want none", len(parts))
	}
}

// TestAZeroPartCountIsRefused: a frame that declares zero parts declares
// nothing, and a decoder that guessed "therefore whole" would be inventing
// what the sender did not say.
func TestAZeroPartCountIsRefused(t *testing.T) {
	a := NewScreenAssembler()
	f := ScreenDataFrame{Session: newID(0xa0), Subscriber: newID(0xb0), Revision: 1, PartIndex: 0, PartCount: 0}
	if _, err := a.Feed(f); !errors.Is(err, ErrScreenAssemblyOrphanPart) {
		t.Fatalf("Feed = %v, want ErrScreenAssemblyOrphanPart", err)
	}
}
