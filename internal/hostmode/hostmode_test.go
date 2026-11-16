package hostmode

// The host record: the integration method by host-key fingerprint
// (ADR-727g63tr decision 1, ADR-0105). Two facts are being kept apart here and
// each test names which one it is about — the MODE a host uses, and the
// MACHINE'S CONSENT, which lives in internal/helper/consent and is tested
// there.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	return NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "host-modes.json"), dir
}

// TestAHostWithNoRecordHasNoMode: a host nobody has answered for is not raw
// and not script — it has no mode at all, and the caller reads that as auto
// (ADR-0033). An empty fingerprint is no machine and never answers.
func TestAHostWithNoRecordHasNoMode(t *testing.T) {
	s, _ := newTestStore(t)
	if mode, ok := s.Lookup("SHA256:abc"); ok {
		t.Fatalf("Lookup on an empty store = %q/%v, want no record", mode, ok)
	}
	if mode, ok := s.Lookup(""); ok {
		t.Fatalf("Lookup on the empty fingerprint = %q/%v, want no record", mode, ok)
	}
}

// TestAnAnswerIsFoundAgainByTheFingerprint: what the connect-time ask writes,
// the open path reads — for a saved connection and a typed one alike, because
// the key is the machine and not the profile.
func TestAnAnswerIsFoundAgainByTheFingerprint(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Set("SHA256:abc", Script); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if mode, ok := s.Lookup("SHA256:abc"); !ok || mode != Script {
		t.Fatalf("Lookup = %q/%v, want script", mode, ok)
	}
	if mode, ok := s.Lookup("SHA256:other"); ok {
		t.Fatalf("Lookup on another machine = %q/%v, want no record — one host's answer is not another's", mode, ok)
	}
}

// TestARecordedAnswerSurvivesANewStore: the document is the contract, not the
// process. A second store over the same directory reads what the first wrote,
// which is what makes "asked once per host" true across a restart.
func TestARecordedAnswerSurvivesANewStore(t *testing.T) {
	first, dir := newTestStore(t)
	if err := first.Set("SHA256:abc", Helper); err != nil {
		t.Fatalf("Set: %v", err)
	}
	second := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "host-modes.json")
	if mode, ok := second.Lookup("SHA256:abc"); !ok || mode != Helper {
		t.Fatalf("a new store read %q/%v, want the recorded helper", mode, ok)
	}
}

// TestACorruptDocumentAnswersNothing: a torn file never answers for a
// machine. Every host reads as not yet answered, which is the ask.
func TestACorruptDocumentAnswersNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "host-modes.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(log.NewSlogAdapter(nil), storage.NewDocumentStore(dir), "host-modes.json")
	if mode, ok := s.Lookup("SHA256:abc"); ok {
		t.Fatalf("corrupt document answered %q/%v, want no record", mode, ok)
	}
}

// TestAutoIsNotAnAnswer: auto means "not yet answered" (ADR-0033), so it is
// never a record. A host that has answered keeps its answer; nothing here may
// turn one into the other.
func TestAutoIsNotAnAnswer(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Set("SHA256:abc", Auto); !errors.Is(err, ErrNotAnAnswer) {
		t.Fatalf("Set(auto) = %v, want ErrNotAnAnswer", err)
	}
	if _, ok := s.Lookup("SHA256:abc"); ok {
		t.Fatal("Set(auto) left a record behind")
	}
}

// TestAnEmptyFingerprintIsNeverARecord: a mode under "" would be every
// machine's mode, which is the defect the fingerprint key exists to prevent.
func TestAnEmptyFingerprintIsNeverARecord(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.Set("", Helper); !errors.Is(err, ErrEmptyFingerprint) {
		t.Fatalf("Set under the empty fingerprint = %v, want ErrEmptyFingerprint", err)
	}
}

// TestAFailedWriteStillAnswersForThisRun is assertion 4's store half: a store
// that cannot persist the answer keeps it in memory — the open the person
// answered for proceeds — and reports ErrNotSaved so the product can say the
// host will have to be asked again. It is NOT rolled back the way a consent
// grant is: a lost answer would put the person back in the same dialog on
// every connect, which is the loop this behaviour exists to end.
func TestAFailedWriteStillAnswersForThisRun(t *testing.T) {
	s := NewStore(log.NewSlogAdapter(nil), refusingStore{}, "host-modes.json")
	err := s.Set("SHA256:abc", Script)
	if !errors.Is(err, ErrNotSaved) {
		t.Fatalf("Set on a store that cannot write = %v, want ErrNotSaved", err)
	}
	if mode, ok := s.Lookup("SHA256:abc"); !ok || mode != Script {
		t.Fatalf("Lookup after a failed write = %q/%v, want the answer kept for this run", mode, ok)
	}
}

// refusingStore is a document store whose writes fail and whose reads find
// nothing — a disk that answered no.
type refusingStore struct{}

func (refusingStore) Read(string, any) (bool, error) { return false, nil }
func (refusingStore) Write(string, any) error        { return errors.New("disk is full") }
func (refusingStore) Delete(string) error            { return errors.New("disk is full") }
