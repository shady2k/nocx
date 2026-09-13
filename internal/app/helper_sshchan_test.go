package app

// The coordinator's re-typing of a helper's channel refusal (nocx-50w7p.3).
//
// It is the one place the migration could go quiet: the helper raises the SAME
// typed errors this process's ssh client raises, and the accept sheet, the
// mismatch warning and the transport's own hostKeyInfoFromError all switch on
// those types. Nothing carries a Go value across the wire, so the types are
// REBUILT here from the refusal's code and evidence — and a rebuild that loses
// a field is a sheet that renders without the fingerprint it is asking about.
//
// These cases are the ones a person meets on first contact with a new host and
// on a changed key, which is to say the two where being wrong is expensive.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/ssh"
)

func refusalWith(code string, details any) error {
	var raw json.RawMessage
	if details != nil {
		b, err := json.Marshal(details)
		if err != nil {
			panic(err)
		}
		raw = b
	}
	return &helperclient.RefusalError{Code: code, Message: "the helper refused", Details: raw}
}

func TestAHostKeyNobodyRecordedSurvivesTheProcessBoundary(t *testing.T) {
	key := []byte("wire-format-public-key")
	h := &sshOverHelper{}
	err := h.translate("host.example.com", refusalWith(string(proto.ProbeHostKeyUnknown), proto.HostKeyEvidence{
		Addr:           "host.example.com:22",
		KnownHostsAddr: "host.example.com:22",
		Algorithm:      "ssh-ed25519",
		Key:            key,
		Fingerprint:    "SHA256:offered",
	}))

	var unknown *ssh.ErrUnknownHostKey
	if !errors.As(err, &unknown) {
		t.Fatalf("translate = %v (%T), want *ssh.ErrUnknownHostKey", err, err)
	}
	if unknown.KnownHostsAddr != "host.example.com:22" {
		t.Fatalf("KnownHostsAddr = %q, want the storage identity the accept path writes back", unknown.KnownHostsAddr)
	}
	if unknown.KeyAlgo != "ssh-ed25519" || unknown.Fingerprint != "SHA256:offered" {
		t.Fatalf("evidence lost: algo %q fingerprint %q", unknown.KeyAlgo, unknown.Fingerprint)
	}
	if string(unknown.Key) != string(key) {
		t.Fatal("the offered key's bytes were lost, so accepting would need a second handshake")
	}
}

func TestAChangedHostKeyCarriesWhatItChangedFrom(t *testing.T) {
	h := &sshOverHelper{}
	err := h.translate("host.example.com", refusalWith(string(proto.ProbeHostKeyChanged), proto.HostKeyEvidence{
		Addr:           "host.example.com:22",
		KnownHostsAddr: "host.example.com:22",
		Algorithm:      "ssh-ed25519",
		Key:            []byte("k"),
		Fingerprint:    "SHA256:offered",
		Expected:       "SHA256:recorded",
	}))

	var changed *ssh.ErrHostKeyMismatch
	if !errors.As(err, &changed) {
		t.Fatalf("translate = %v (%T), want *ssh.ErrHostKeyMismatch", err, err)
	}
	// The expected value is the point of the whole case: a changed key
	// rendered without the value it changed FROM is a warning nobody can act
	// on, and that is the one signature of a machine in the middle.
	if changed.Expected != "SHA256:recorded" || changed.Fingerprint != "SHA256:offered" {
		t.Fatalf("expected %q offered %q, want both fingerprints intact", changed.Expected, changed.Fingerprint)
	}
}

// TestARefusedChannelIsTheCoordinatorsOwnRefusal: the file and install paths
// already have a sentence for "this host will not serve that channel", and a
// migration that reported it as a helper failure would have two vocabularies
// for one fact.
func TestARefusedChannelIsTheCoordinatorsOwnRefusal(t *testing.T) {
	h := &sshOverHelper{}
	err := h.translate("host.example.com", refusalWith(proto.ErrCodeChannelRefused, nil))
	if !errors.Is(err, ssh.ErrFSSubsystemRefused) {
		t.Fatalf("translate = %v, want a refusal wrapping ErrFSSubsystemRefused", err)
	}
}

// TestAnUnreachableHostKeepsItsOwnSentence: the class a person acts on is in
// the sentence, and it must not be replaced by the act that failed.
func TestAnUnreachableHostKeepsItsOwnSentence(t *testing.T) {
	h := &sshOverHelper{}
	err := h.translate("host.example.com", &helperclient.RefusalError{
		Code:    string(proto.ProbeUnreachable),
		Message: "dial tcp 10.0.0.1:22: connection refused",
	})
	if err == nil {
		t.Fatal("an unreachable host translated to nil")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("translate = %q, want the helper's own sentence in it", err.Error())
	}
	if !strings.Contains(err.Error(), "host.example.com") {
		t.Fatalf("translate = %q, want the destination named", err.Error())
	}
}

// TestAHostKeyRefusalWithNoEvidenceIsStillARefusal: a payload that will not
// decode must degrade to a plain error and never to a typed one carrying empty
// fields — an accept sheet built from a zero-value error would offer to record
// a key nobody saw.
func TestAHostKeyRefusalWithNoEvidenceIsStillARefusal(t *testing.T) {
	h := &sshOverHelper{}
	err := h.translate("host.example.com", refusalWith(string(proto.ProbeHostKeyUnknown), nil))
	var unknown *ssh.ErrUnknownHostKey
	if errors.As(err, &unknown) {
		t.Fatalf("a refusal with no evidence became an accept sheet: %+v", unknown)
	}
	if err == nil {
		t.Fatal("translate returned nil")
	}
}
