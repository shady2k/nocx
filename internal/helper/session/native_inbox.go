package session

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sandbox"
)

const (
	nativeInboxCapacity        = 500
	nativeInboxPageLimit       = 200
	nativeInboxQueueCapacity   = 64
	nativeInboxWorkers         = 4
	nativeInboxResolutionLimit = 32
)

var (
	errDiagnosticConflict = errors.New("sandbox diagnostic conflict")
	errDiagnosticUnknown  = errors.New("sandbox diagnostic has no directory proposal")
	errDiagnosticRetarget = errors.New("sandbox diagnostic target changed")
	errDiagnosticPending  = errors.New("sandbox diagnostic resolution pending")
)

type nativeDiagnosticKey struct {
	executable string
	path       string
	operation  string
	access     sandbox.DiagnosticAccess
}

type nativeProposal struct {
	wire            sandbox.DiagnosticProposal
	observed        string
	canonicalTarget string
	target          fs.FileInfo
	directory       fs.FileInfo
}

type nativeDiagnosticRecord struct {
	wire             sandbox.DiagnosticRecord
	proposal         *nativeProposal
	reservation      string
	decision         sandbox.DiagnosticDecision
	resolvedRevision uint64
}

// nativeInbox belongs to one helper-owned process, not a renderer connection.
// Only its four workers perform prediction/filesystem IO; Observe never waits.
type nativeInbox struct {
	mu          sync.Mutex
	policy      sandbox.Policy
	prefix      string
	observer    sandbox.ObserverStatus
	revision    uint64
	records     [nativeInboxCapacity]nativeDiagnosticRecord
	count       int
	byKey       map[nativeDiagnosticKey]int
	byID        map[string]int
	resolving   int
	queue       chan sandbox.DiagnosticObservation
	stop        chan struct{}
	noticeWake  chan struct{}
	notice      func(proto.SandboxAccessChanged)
	noticeValue proto.SandboxAccessChanged
	closed      atomic.Bool
	dropped     atomic.Uint64
	closeOnce   sync.Once
	workers     sync.WaitGroup
}

func newNativeInbox(policy sandbox.Policy, sessionID string) *nativeInbox {
	inbox := &nativeInbox{
		policy: policy, prefix: sessionID, observer: sandbox.ObserverUnavailable,
		byKey:      make(map[nativeDiagnosticKey]int, nativeInboxCapacity),
		byID:       make(map[string]int, nativeInboxCapacity),
		queue:      make(chan sandbox.DiagnosticObservation, nativeInboxQueueCapacity),
		stop:       make(chan struct{}),
		noticeWake: make(chan struct{}, 1),
	}
	inbox.workers.Add(nativeInboxWorkers + 1)
	for range nativeInboxWorkers {
		go inbox.consume()
	}
	go inbox.deliverNotices()
	return inbox
}

// bindNotice attaches the helper's metadata-only carrier after the launch
// identity is known. The initial snapshot closes the collector-start race.
func (i *nativeInbox) bindNotice(session proto.HostSessionID, launchID string, notify func(proto.SandboxAccessChanged)) {
	i.mu.Lock()
	i.notice = notify
	i.noticeValue.Session = session
	i.noticeValue.LaunchID = launchID
	i.mu.Unlock()
	i.signalChanged()
}

func (i *nativeInbox) signalChanged() {
	i.mu.Lock()
	if i.notice == nil {
		i.mu.Unlock()
		return
	}
	i.noticeValue.Revision = i.revision
	i.noticeValue.Dropped = i.dropped.Load()
	i.noticeValue.Observer = i.observer
	i.noticeValue.Total = uint16(i.count) //nolint:gosec // count is bounded by the fixed 500-record inbox.
	i.mu.Unlock()
	select {
	case i.noticeWake <- struct{}{}:
	default:
	}
}

func (i *nativeInbox) deliverNotices() {
	defer i.workers.Done()
	timer := time.NewTimer(50 * time.Millisecond)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-i.stop:
			return
		case <-i.noticeWake:
			timer.Reset(50 * time.Millisecond)
			select {
			case <-i.stop:
				timer.Stop()
				return
			case <-timer.C:
			}
			select {
			case <-i.noticeWake:
			default:
			}
			i.mu.Lock()
			notify, notice := i.notice, i.noticeValue
			i.mu.Unlock()
			if notify != nil {
				notify(notice)
			}
		}
	}
}

func (i *nativeInbox) Observe(observation sandbox.DiagnosticObservation) {
	if i.closed.Load() {
		return
	}
	select {
	case i.queue <- observation:
	default:
		i.Drop(1)
	}
}

func (i *nativeInbox) Drop(count uint64) {
	if count == 0 {
		return
	}
	i.dropped.Add(count)
	i.mu.Lock()
	i.revision++
	i.mu.Unlock()
	i.signalChanged()
}

func (i *nativeInbox) SetObserver(status sandbox.ObserverStatus) {
	i.mu.Lock()
	changed := i.observer != status
	if changed {
		i.observer = status
		i.revision++
	}
	i.mu.Unlock()
	if changed {
		i.signalChanged()
	}
}

func (i *nativeInbox) observerStatus() sandbox.ObserverStatus {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.observer
}

// stopWorkers preserves the bounded history after shell exit. Session disposal
// releases it; coordinator loss alone does neither.
func (i *nativeInbox) stopWorkers() {
	i.closeOnce.Do(func() {
		i.closed.Store(true)
		close(i.stop)
		i.workers.Wait()
		for {
			select {
			case <-i.queue:
				i.Drop(1)
			default:
				return
			}
		}
	})
}

func (i *nativeInbox) consume() {
	defer i.workers.Done()
	for {
		select {
		case <-i.stop:
			return
		case observation := <-i.queue:
			i.record(observation)
		}
	}
}

func (i *nativeInbox) record(observation sandbox.DiagnosticObservation) {
	if len(observation.Path) > sandbox.MaxPathBytes || len(observation.Executable) > sandbox.MaxPathBytes || len(observation.Operation) > 128 {
		i.Drop(1)
		return
	}
	prediction := predictNativeAccess(i.policy, observation)
	if observation.Source == sandbox.DiagnosticLinuxSeccomp && prediction == sandbox.PredictionAllowed {
		return
	}
	key := nativeDiagnosticKey{observation.Executable, observation.Path, observation.Operation, observation.Access}
	i.mu.Lock()
	if index, exists := i.byKey[key]; exists {
		record := &i.records[index]
		record.wire.Count++
		record.wire.Revision++
		i.revision++
		i.mu.Unlock()
		i.signalChanged()
		return
	}
	if i.count == nativeInboxCapacity {
		i.mu.Unlock()
		i.Drop(1)
		return
	}
	i.mu.Unlock()
	var proposal *nativeProposal
	if observation.PathKnown && observation.Access != sandbox.DiagnosticUnknown && observation.Precision != sandbox.PrecisionUnknown {
		proposal = prepareNativeProposal(observation.Path)
	}
	i.mu.Lock()
	if index, exists := i.byKey[key]; exists {
		i.records[index].wire.Count++
		i.records[index].wire.Revision++
		i.revision++
		i.mu.Unlock()
		i.signalChanged()
		return
	}
	if i.count == nativeInboxCapacity {
		i.mu.Unlock()
		i.Drop(1)
		return
	}
	index := i.count
	id := i.prefix + ":" + strconv.Itoa(index+1)
	record := nativeDiagnosticRecord{wire: sandbox.DiagnosticRecord{
		ID: id, Revision: 1, Executable: observation.Executable,
		Path: observation.Path, Operation: observation.Operation, Access: observation.Access,
		PathKnown: observation.PathKnown, Source: observation.Source, Precision: observation.Precision,
		Prediction: prediction, Count: 1, State: sandbox.DiagnosticUnresolved,
	}, proposal: proposal}
	if proposal != nil {
		copy := proposal.wire
		record.wire.Proposal = &copy
	}
	i.records[index] = record
	i.byKey[key] = index
	i.byID[id] = index
	i.count++
	i.revision++
	i.mu.Unlock()
	i.signalChanged()
}

func (i *nativeInbox) page(cursor, limit uint16) sandbox.DiagnosticPage {
	if limit == 0 || limit > nativeInboxPageLimit {
		limit = nativeInboxPageLimit
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	start := min(int(cursor), i.count)
	end := min(start+int(limit), i.count)
	out := sandbox.DiagnosticPage{Observer: i.observer, Revision: i.revision, Dropped: i.dropped.Load(), Total: uint16(i.count), Records: make([]sandbox.DiagnosticRecord, 0, end-start)} //nolint:gosec // count is at most 500.
	if end < i.count {
		out.NextCursor = uint16(end) //nolint:gosec // end is bounded by count, at most 500.
	}
	for index := start; index < end; index++ {
		row := i.records[index].wire
		if row.Proposal != nil {
			copy := *row.Proposal
			row.Proposal = &copy
		}
		out.Records = append(out.Records, row)
	}
	return out
}

func (i *nativeInbox) reserve(id string, revision uint64, decision sandbox.DiagnosticDecision) (string, sandbox.DiagnosticRecord, error) {
	i.mu.Lock()
	index, exists := i.byID[id]
	if !exists {
		i.mu.Unlock()
		return "", sandbox.DiagnosticRecord{}, errDiagnosticConflict
	}
	record := &i.records[index]
	if record.wire.State != sandbox.DiagnosticUnresolved {
		if record.decision == decision && (record.wire.State == sandbox.DiagnosticDismissed || record.wire.State == sandbox.DiagnosticFuturePolicy) {
			row := record.wire
			i.mu.Unlock()
			return "", row, nil
		}
		i.mu.Unlock()
		return "", sandbox.DiagnosticRecord{}, errDiagnosticPending
	}
	if record.wire.Revision != revision {
		i.mu.Unlock()
		return "", sandbox.DiagnosticRecord{}, errDiagnosticConflict
	}
	if decision == sandbox.DecisionDismiss {
		record.decision = decision
		record.wire.State = sandbox.DiagnosticDismissed
		record.wire.Revision++
		i.revision++
		row := record.wire
		i.mu.Unlock()
		i.signalChanged()
		return "", row, nil
	}
	if decision != sandbox.DecisionAllowRO && decision != sandbox.DecisionAllowRW {
		i.mu.Unlock()
		return "", sandbox.DiagnosticRecord{}, errDiagnosticConflict
	}
	proposal := record.proposal
	if proposal == nil {
		i.mu.Unlock()
		return "", sandbox.DiagnosticRecord{}, errDiagnosticUnknown
	}
	if i.resolving >= nativeInboxResolutionLimit {
		i.mu.Unlock()
		return "", sandbox.DiagnosticRecord{}, errSandboxCapacity
	}
	i.mu.Unlock()
	current := prepareNativeProposal(proposal.observed)
	if !sameNativeProposal(proposal, current) {
		i.mu.Lock()
		if record.wire.Revision == revision && record.wire.State == sandbox.DiagnosticUnresolved {
			record.proposal = current
			record.wire.Proposal = nil
			if current != nil {
				copy := current.wire
				record.wire.Proposal = &copy
			}
			record.wire.Revision++
			i.revision++
		}
		i.mu.Unlock()
		i.signalChanged()
		return "", sandbox.DiagnosticRecord{}, errDiagnosticRetarget
	}
	token := rand.Text()
	i.mu.Lock()
	if record.wire.Revision != revision || record.wire.State != sandbox.DiagnosticUnresolved {
		i.mu.Unlock()
		return "", sandbox.DiagnosticRecord{}, errDiagnosticConflict
	}
	if i.resolving >= nativeInboxResolutionLimit {
		i.mu.Unlock()
		return "", sandbox.DiagnosticRecord{}, errSandboxCapacity
	}
	record.reservation, record.decision = token, decision
	record.wire.State = sandbox.DiagnosticPending
	i.resolving++
	i.revision++
	row := record.wire
	i.mu.Unlock()
	i.signalChanged()
	return token, row, nil
}

func (i *nativeInbox) finish(id, token string, committed bool, profileRevision uint64) (sandbox.DiagnosticRecord, error) {
	i.mu.Lock()
	index, exists := i.byID[id]
	if !exists {
		i.mu.Unlock()
		return sandbox.DiagnosticRecord{}, errDiagnosticConflict
	}
	record := &i.records[index]
	if record.reservation != token || token == "" {
		i.mu.Unlock()
		return sandbox.DiagnosticRecord{}, errDiagnosticConflict
	}
	if record.wire.State == sandbox.DiagnosticFuturePolicy && committed && record.resolvedRevision == profileRevision {
		row := record.wire
		i.mu.Unlock()
		return row, nil
	}
	if record.wire.State != sandbox.DiagnosticPending {
		i.mu.Unlock()
		return sandbox.DiagnosticRecord{}, errDiagnosticConflict
	}
	i.resolving--
	if committed {
		record.wire.State = sandbox.DiagnosticFuturePolicy
		record.resolvedRevision = profileRevision
		record.wire.FutureRevision = profileRevision
	} else {
		record.wire.State = sandbox.DiagnosticUnresolved
		record.reservation = ""
	}
	record.wire.Revision++
	i.revision++
	row := record.wire
	i.mu.Unlock()
	i.signalChanged()
	return row, nil
}

// markUncertain quarantines a reservation after ambiguous profile IO. It never
// releases the slot for another CAS and cannot downgrade a committed receipt.
func (i *nativeInbox) markUncertain(id, token string) (sandbox.DiagnosticRecord, error) {
	i.mu.Lock()
	index, exists := i.byID[id]
	if !exists || token == "" {
		i.mu.Unlock()
		return sandbox.DiagnosticRecord{}, errDiagnosticConflict
	}
	record := &i.records[index]
	if record.reservation != token {
		i.mu.Unlock()
		return sandbox.DiagnosticRecord{}, errDiagnosticConflict
	}
	if record.wire.State == sandbox.DiagnosticPending {
		record.wire.State = sandbox.DiagnosticUncertain
		record.wire.Revision++
		i.revision++
	} else if record.wire.State != sandbox.DiagnosticFuturePolicy && record.wire.State != sandbox.DiagnosticUncertain {
		i.mu.Unlock()
		return sandbox.DiagnosticRecord{}, errDiagnosticConflict
	}
	row := record.wire
	i.mu.Unlock()
	i.signalChanged()
	return row, nil
}

func predictNativeAccess(policy sandbox.Policy, observation sandbox.DiagnosticObservation) sandbox.DiagnosticPrediction {
	if !observation.PathKnown || observation.Access == sandbox.DiagnosticUnknown || !filepath.IsAbs(observation.Path) || filepath.Clean(observation.Path) != observation.Path {
		return sandbox.PredictionUnknown
	}
	for _, root := range policy.Roots {
		matches := observation.Path == root.Path
		if root.Kind == sandbox.DirectoryRoot {
			matches = matches || root.Path == string(filepath.Separator) || strings.HasPrefix(observation.Path, root.Path+string(filepath.Separator))
		}
		if matches && (root.Access == sandbox.ReadWrite || observation.Access == sandbox.DiagnosticRead) {
			return sandbox.PredictionAllowed
		}
	}
	return sandbox.PredictionDenied
}

func prepareNativeProposal(observed string) *nativeProposal {
	if !filepath.IsAbs(observed) || filepath.Clean(observed) != observed || len(observed) > sandbox.MaxPathBytes || strings.IndexByte(observed, 0) >= 0 {
		return nil
	}
	canonical, err := filepath.EvalSymlinks(observed)
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		return nil
	}
	var target fs.FileInfo
	directory := canonical
	basis := "directory"
	if !missing {
		target, err = os.Stat(canonical)
		if err != nil || (!target.IsDir() && !target.Mode().IsRegular()) {
			return nil
		}
		if !target.IsDir() {
			directory = filepath.Dir(canonical)
			basis = "parent"
		}
	} else {
		if _, statErr := os.Lstat(observed); !errors.Is(statErr, fs.ErrNotExist) {
			return nil
		}
		// Only the immediately existing parent is proposed. Missing ancestors
		// are never guessed or widened to the nearest existing tree.
		directory, err = filepath.EvalSymlinks(filepath.Dir(observed))
		if err != nil {
			return nil
		}
		canonical = filepath.Join(directory, filepath.Base(observed))
		basis = "parent"
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil
	}
	return &nativeProposal{wire: sandbox.DiagnosticProposal{Directory: directory, Basis: basis, MissingTarget: missing}, observed: observed, canonicalTarget: canonical, target: target, directory: info}
}

func sameNativeProposal(before, after *nativeProposal) bool {
	if before == nil || after == nil || before.observed != after.observed || before.canonicalTarget != after.canonicalTarget || before.wire != after.wire || !os.SameFile(before.directory, after.directory) {
		return false
	}
	if before.target == nil || after.target == nil {
		return before.target == nil && after.target == nil
	}
	return os.SameFile(before.target, after.target)
}
