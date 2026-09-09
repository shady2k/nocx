package agentcalib

// Where a labelled set lives: local files under the app directory, one
// directory per agent, readable by hand (the configuration design §6).
//
// The app directory is chosen by the BUILD (internal/storage/appdir.go), so a
// dev stand and the shipped app keep their own sets and neither an e2e run nor
// `wails dev` can overwrite a calibration somebody depends on.
//
// Two files, because they are two formats and not one:
//
//	<config>/agents/calibration/<agent>/capture.jsonl   the frames, as a capture
//	<config>/agents/calibration/<agent>/labels.json     the marks, and which
//	                                                    steps were declined
//
// The capture is exactly what cmd/agent-capture writes and reads, which is
// what lets a person inspect a set with the command they already have. The
// geometry lives only in its header — labels.json does not restate it, because
// a second copy is a second thing to be wrong.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/shady2k/nocx/internal/agentcapture"
)

// Store is the calibration set's persistence seam (AD-8).
type Store interface {
	// Load reads the set for an agent. Not found is a normal answer: most
	// agents have never been calibrated.
	Load(agent string) (Set, bool, error)
	// Save writes a set whole, replacing whatever was there.
	Save(set Set) error
}

// FileStore is the on-disk implementation.
type FileStore struct{ root string }

// NewFileStore roots a store at a directory the caller owns — in the product,
// the app's config directory.
func NewFileStore(root string) (*FileStore, error) {
	if root == "" {
		return nil, errors.New("agentcalib: file store needs a root directory")
	}
	return &FileStore{root: root}, nil
}

// agentName bounds what may become a directory name. The agent is named by the
// enrolment act, which carries a string across the wire, and a string that
// walks out of the app directory is a wiring mistake with a filesystem behind
// it. Leading dots are out too: a set nobody can see is a set nobody can
// repair by hand, which is the whole reason these are files.
var agentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validAgent(agent string) error {
	if !agentName.MatchString(agent) {
		return fmt.Errorf(
			"agentcalib: %q is not an agent name (letters, digits, dot, dash and underscore, up to 64, not starting with a dot)",
			agent)
	}
	return nil
}

const (
	captureFile = "capture.jsonl"
	labelsFile  = "labels.json"
	// labelsVersion is this document's own schema version. Modules own their
	// version numbers (ADR-0011 §6); there is no app-wide one.
	labelsVersion = 1
)

// labelsDoc is the on-disk shape. It deliberately carries no completeness
// claim: Set.Complete is derived, so there is nothing here for an editor to
// contradict.
type labelsDoc struct {
	Version int      `json:"version"`
	Agent   string   `json:"agent"`
	Labels  []Record `json:"labels"`
}

func (s *FileStore) dir(agent string) (string, error) {
	if err := validAgent(agent); err != nil {
		return "", err
	}
	return filepath.Join(s.root, "agents", "calibration", agent), nil
}

// Load reads a set back. A directory that holds one file and not the other is
// a refusal rather than half a set: marks with no capture replay to nothing,
// and a capture with no marks labels nothing.
func (s *FileStore) Load(agent string) (Set, bool, error) {
	dir, err := s.dir(agent)
	if err != nil {
		return Set{}, false, err
	}
	data, err := os.ReadFile(filepath.Join(dir, labelsFile)) //nolint:gosec // a path this store owns
	if errors.Is(err, os.ErrNotExist) {
		return Set{}, false, nil
	}
	if err != nil {
		return Set{}, false, fmt.Errorf("agentcalib: read %s labels: %w", agent, err)
	}
	var doc labelsDoc
	if decodeErr := json.Unmarshal(data, &doc); decodeErr != nil {
		return Set{}, false, fmt.Errorf("agentcalib: decode %s labels: %w", agent, decodeErr)
	}
	if doc.Version != labelsVersion {
		return Set{}, false, fmt.Errorf(
			"agentcalib: %s labels are version %d and this build reads version %d",
			agent, doc.Version, labelsVersion)
	}
	header, chunks, err := agentcapture.Read(filepath.Join(dir, captureFile))
	if err != nil {
		return Set{}, false, fmt.Errorf("agentcalib: read %s capture: %w", agent, err)
	}
	return Set{Agent: agent, Header: header, Chunks: chunks, Labels: doc.Labels}, true, nil
}

// Save writes the capture first and the labels second, so an interrupted save
// leaves marks that point into the capture they were taken from rather than
// into an older one.
func (s *FileStore) Save(set Set) error {
	dir, err := s.dir(set.Agent)
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return fmt.Errorf("agentcalib: create %s directory: %w", set.Agent, mkErr)
	}
	if writeErr := agentcapture.Write(filepath.Join(dir, captureFile), set.Header, set.Chunks); writeErr != nil {
		return fmt.Errorf("agentcalib: write %s capture: %w", set.Agent, writeErr)
	}
	doc := labelsDoc{Version: labelsVersion, Agent: set.Agent, Labels: set.Labels}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("agentcalib: encode %s labels: %w", set.Agent, err)
	}
	tmp := filepath.Join(dir, labelsFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("agentcalib: write %s labels: %w", set.Agent, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, labelsFile)); err != nil {
		return fmt.Errorf("agentcalib: place %s labels: %w", set.Agent, err)
	}
	return nil
}
