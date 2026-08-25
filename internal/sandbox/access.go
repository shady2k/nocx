package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	accessInboxCapacity = 500
	accessListMax       = 200
	accessPathMaxBytes  = 16 * 1024
)

type AccessClass string

const (
	AccessReadOnly  AccessClass = "readOnly"
	AccessReadWrite AccessClass = "readWrite"
)

type AccessDecision string

const (
	AccessDecisionDismiss            AccessDecision = "dismiss"
	AccessDecisionWorkspaceReadOnly  AccessDecision = "workspaceReadOnly"
	AccessDecisionWorkspaceReadWrite AccessDecision = "workspaceReadWrite"
)

type AccessState string

const (
	AccessStatePending   AccessState = "pending"
	AccessStateDismissed AccessState = "dismissed"
	AccessStateGranted   AccessState = "granted"
	AccessStateExpired   AccessState = "expired"
)

type AccessSource string

const (
	AccessSourceLinuxSeccomp   AccessSource = "linux-seccomp-user-notify"
	AccessSourceDarwinSeatbelt AccessSource = "darwin-seatbelt-log"
)

type SessionIdentity struct {
	SessionID  string `json:"sessionId"`
	InstanceID string `json:"instanceId"`
	Epoch      uint64 `json:"sessionEpoch"`
}

func (i SessionIdentity) valid() bool {
	return i.SessionID != "" && i.InstanceID != "" && i.Epoch != 0
}

type AccessObservation struct {
	Identity SessionIdentity
	// PaneID and WorkspaceID are backend-owned provenance: the pane the
	// denied process's session is the pipe of, and the layout workspace that
	// pane belongs to. They never come from the renderer.
	PaneID      string
	WorkspaceID string
	Shell       string
	Executable  string
	Path        string
	Access      AccessClass
	Operation   string
	Source      AccessSource
	At          time.Time
}

type AccessEvent struct {
	ID           string `json:"id"`
	SessionID    string `json:"sessionId"`
	InstanceID   string `json:"instanceId"`
	SessionEpoch uint64 `json:"sessionEpoch"`
	// PaneID and WorkspaceID are backend-owned provenance carried beside the
	// session identity (design 2026-08-23 §4.4). They are never supplied by
	// the renderer and never redirect authority.
	PaneID          string         `json:"paneId"`
	WorkspaceID     string         `json:"workspaceId"`
	Shell           string         `json:"shell,omitempty"`
	Executable      string         `json:"executable,omitempty"`
	Path            string         `json:"path"`
	Directory       string         `json:"directory"`
	CanGrant        bool           `json:"canGrant"`
	GrantReason     string         `json:"grantReason,omitempty"`
	Access          AccessClass    `json:"access"`
	Operation       string         `json:"operation,omitempty"`
	Source          AccessSource   `json:"source"`
	FirstSeen       time.Time      `json:"firstSeen"`
	LastSeen        time.Time      `json:"lastSeen"`
	Count           uint32         `json:"count"`
	State           AccessState    `json:"state"`
	Decision        AccessDecision `json:"decision,omitempty"`
	ProfileRevision int64          `json:"profileRevision,omitempty"`
}

type AccessListOptions struct {
	Limit int
}

type AccessPage struct {
	Events   []AccessEvent `json:"events"`
	Revision uint64        `json:"revision"`
	Lost     uint64        `json:"lost"`
}

type AccessResolveRequest struct {
	EventID  string
	Decision AccessDecision
}

type AccessMonitorStatus struct {
	Available bool   `json:"available"`
	Platform  string `json:"platform"`
	Backend   string `json:"backend,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Lost      uint64 `json:"lost"`
}

type AccessGrantStore interface {
	// PromoteSandboxPath atomically appends a validated directory to the
	// profile of the backend-owned workspace the event belongs to. A
	// default-workspace event appends to the standard profile; a named
	// workspace without a profile receives a copy-on-write profile. It
	// returns the profile revision after the write.
	PromoteSandboxPath(workspaceID string, access AccessClass, path string) (revision int64, err error)
}

var (
	ErrAccessEventNotFound    = errors.New("sandbox access event not found")
	ErrAccessEventResolved    = errors.New("sandbox access event already resolved")
	ErrAccessGrantUnavailable = errors.New("sandbox access grant unavailable")
	ErrInvalidAccessDecision  = errors.New("invalid sandbox access decision")
)

type AccessInbox struct {
	mu          sync.Mutex
	grant       AccessGrantStore
	events      []AccessEvent
	keys        map[string]int
	grants      map[string]accessGrantCheck
	resolving   map[string]struct{}
	revision    uint64
	lost        uint64
	capacity    int
	status      AccessMonitorStatus
	watchers    map[uint64]func(uint64)
	nextWatcher uint64
}

type accessGrantCheck struct {
	lexical   string
	canonical string
	info      os.FileInfo
}

func (c accessGrantCheck) validNow() bool {
	info, err := os.Stat(c.lexical)
	if err != nil || !info.IsDir() || !os.SameFile(c.info, info) {
		return false
	}
	canonical, err := filepath.EvalSymlinks(c.lexical)
	return err == nil && filepath.Clean(canonical) == c.canonical
}

type AccessSession struct {
	mu       sync.Mutex
	inbox    *AccessInbox
	identity SessionIdentity
	// paneID and workspaceID are backend-owned provenance recorded on every
	// observation this session raises.
	paneID      string
	workspaceID string
	active      bool
	closed      bool
	pending     []AccessObservation
}

func (i *AccessInbox) BeginSession(identity SessionIdentity, paneID, workspaceID string) *AccessSession {
	if i == nil || !identity.valid() {
		return nil
	}
	return &AccessSession{
		inbox: i, identity: identity, paneID: paneID, workspaceID: workspaceID,
		pending: make([]AccessObservation, 0, 16),
	}
}

func (s *AccessSession) Record(observation AccessObservation) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.inbox.noteLost()
		return
	}
	observation.Identity = s.identity
	observation.PaneID = s.paneID
	observation.WorkspaceID = s.workspaceID
	if !s.active {
		if len(s.pending) >= 32 {
			s.mu.Unlock()
			s.inbox.noteLost()
			return
		}
		s.pending = append(s.pending, observation)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.inbox.Record(observation)
}

func (s *AccessSession) noteLost() {
	if s == nil {
		return
	}
	s.inbox.noteLost()
}

func (s *AccessSession) Activate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.active {
		return
	}
	s.active = true
	pending := s.pending
	s.pending = nil
	for _, observation := range pending {
		s.inbox.Record(observation)
	}
}

func (s *AccessSession) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	dropped := len(s.pending)
	s.pending = nil
	s.mu.Unlock()
	if dropped > 0 {
		s.inbox.addLost(uint64(dropped))
	}
	s.inbox.CloseSession(s.identity)
}

func NewAccessInbox(grant AccessGrantStore) *AccessInbox {
	return &AccessInbox{grant: grant, keys: make(map[string]int), grants: make(map[string]accessGrantCheck), resolving: make(map[string]struct{}), capacity: accessInboxCapacity, watchers: make(map[uint64]func(uint64))}
}

func (i *AccessInbox) SetGrantStore(grant AccessGrantStore) {
	i.mu.Lock()
	i.grant = grant
	i.mu.Unlock()
}

func (i *AccessInbox) SetStatus(status AccessMonitorStatus) {
	i.mu.Lock()
	status.Lost = i.lost
	i.status = status
	i.revision++
	rev, callbacks := i.snapshotWatchersLocked()
	i.mu.Unlock()
	notifyAccessWatchers(callbacks, rev)
}

func (i *AccessInbox) Status() AccessMonitorStatus {
	i.mu.Lock()
	defer i.mu.Unlock()
	status := i.status
	status.Lost = i.lost
	return status
}

func (i *AccessInbox) noteLost() {
	i.addLost(1)
}

func (i *AccessInbox) addLost(count uint64) {
	if i == nil || count == 0 {
		return
	}
	i.mu.Lock()
	if ^uint64(0)-i.lost < count {
		i.lost = ^uint64(0)
	} else {
		i.lost += count
	}
	i.revision++
	rev, callbacks := i.snapshotWatchersLocked()
	i.mu.Unlock()
	notifyAccessWatchers(callbacks, rev)
}

func (i *AccessInbox) Subscribe(fn func(uint64)) func() {
	if fn == nil {
		return func() {}
	}
	i.mu.Lock()
	i.nextWatcher++
	id := i.nextWatcher
	i.watchers[id] = fn
	i.mu.Unlock()
	return func() {
		i.mu.Lock()
		delete(i.watchers, id)
		i.mu.Unlock()
	}
}

func (i *AccessInbox) Record(observation AccessObservation) {
	if !observation.Identity.valid() || !filepath.IsAbs(observation.Path) ||
		!validAccessText(observation.Path, accessPathMaxBytes, false) ||
		!validAccessText(observation.Shell, 4096, true) ||
		!validAccessText(observation.Executable, 4096, true) ||
		!validAccessText(observation.Operation, 128, true) {
		i.noteLost()
		return
	}
	if observation.Access != AccessReadOnly && observation.Access != AccessReadWrite {
		i.noteLost()
		return
	}
	if observation.Source != AccessSourceLinuxSeccomp && observation.Source != AccessSourceDarwinSeatbelt {
		i.noteLost()
		return
	}
	observation.Path = filepath.Clean(observation.Path)
	if observation.At.IsZero() {
		observation.At = time.Now().UTC()
	}
	directory, grantCheck := accessGrantDirectory(observation.Path)
	key := accessEventKey(observation)

	i.mu.Lock()
	if index, ok := i.keys[key]; ok && index < len(i.events) && i.events[index].State == AccessStatePending {
		event := &i.events[index]
		event.LastSeen = observation.At
		if event.Count < ^uint32(0) {
			event.Count++
		}
		i.revision++
		rev, callbacks := i.snapshotWatchersLocked()
		i.mu.Unlock()
		notifyAccessWatchers(callbacks, rev)
		return
	}
	id, err := newAccessEventID()
	if err != nil {
		i.mu.Unlock()
		i.noteLost()
		return
	}
	if len(i.events) >= i.capacity {
		i.dropOldestLocked()
	}
	i.events = append(i.events, AccessEvent{
		ID: id, SessionID: observation.Identity.SessionID, InstanceID: observation.Identity.InstanceID,
		SessionEpoch: observation.Identity.Epoch, PaneID: observation.PaneID, WorkspaceID: observation.WorkspaceID,
		Shell: observation.Shell, Executable: observation.Executable,
		Path: observation.Path, Directory: directory, CanGrant: directory != "", Access: observation.Access, Operation: observation.Operation,
		Source: observation.Source, FirstSeen: observation.At, LastSeen: observation.At, Count: 1, State: AccessStatePending,
	})
	if directory == "" {
		i.events[len(i.events)-1].GrantReason = "No existing directory can be granted without widening the attempted path."
	}
	if grantCheck != nil {
		i.grants[id] = *grantCheck
	}
	i.rebuildKeysLocked()
	i.revision++
	rev, callbacks := i.snapshotWatchersLocked()
	i.mu.Unlock()
	notifyAccessWatchers(callbacks, rev)
}

func (i *AccessInbox) List(opts AccessListOptions) AccessPage {
	i.mu.Lock()
	defer i.mu.Unlock()
	limit := opts.Limit
	if limit <= 0 || limit > accessListMax {
		limit = accessListMax
	}
	start := len(i.events) - limit
	if start < 0 {
		start = 0
	}
	events := append(make([]AccessEvent, 0, len(i.events)-start), i.events[start:]...)
	sort.SliceStable(events, func(a, b int) bool { return events[a].LastSeen.After(events[b].LastSeen) })
	return AccessPage{Events: events, Revision: i.revision, Lost: i.lost}
}

func (i *AccessInbox) Resolve(req AccessResolveRequest) (AccessEvent, error) {
	i.mu.Lock()
	index := -1
	for n := range i.events {
		if i.events[n].ID == req.EventID {
			index = n
			break
		}
	}
	if index < 0 {
		i.mu.Unlock()
		return AccessEvent{}, ErrAccessEventNotFound
	}
	event := &i.events[index]
	if event.State != AccessStatePending {
		i.mu.Unlock()
		return AccessEvent{}, ErrAccessEventResolved
	}
	if _, resolving := i.resolving[event.ID]; resolving {
		i.mu.Unlock()
		return AccessEvent{}, ErrAccessEventResolved
	}

	if req.Decision == AccessDecisionDismiss {
		event.State = AccessStateDismissed
		event.Decision = req.Decision
		i.revision++
		rev, callbacks := i.snapshotWatchersLocked()
		resolved := *event
		i.mu.Unlock()
		go notifyAccessWatchers(callbacks, rev)
		return resolved, nil
	}

	if req.Decision != AccessDecisionWorkspaceReadOnly && req.Decision != AccessDecisionWorkspaceReadWrite {
		i.mu.Unlock()
		return AccessEvent{}, ErrInvalidAccessDecision
	}
	if !event.CanGrant || event.Directory == "" {
		i.mu.Unlock()
		return AccessEvent{}, ErrAccessGrantUnavailable
	}
	check, ok := i.grants[event.ID]
	if !ok || !check.validNow() {
		event.CanGrant = false
		event.GrantReason = "The directory changed or no longer exists; granting it would be unsafe."
		i.revision++
		rev, callbacks := i.snapshotWatchersLocked()
		i.mu.Unlock()
		go notifyAccessWatchers(callbacks, rev)
		return AccessEvent{}, ErrAccessGrantUnavailable
	}
	if i.grant == nil {
		i.mu.Unlock()
		return AccessEvent{}, errors.New("sandbox access grant store unavailable")
	}
	access := AccessReadOnly
	if req.Decision == AccessDecisionWorkspaceReadWrite {
		access = AccessReadWrite
	}
	if access == AccessReadWrite {
		systemRoots, rootsErr := canonicalSystemRoots()
		if rootsErr != nil || writableRootIsProtected(event.Directory, systemRoots) {
			event.CanGrant = false
			event.GrantReason = "The directory would make a protected system root writable."
			i.revision++
			rev, callbacks := i.snapshotWatchersLocked()
			i.mu.Unlock()
			go notifyAccessWatchers(callbacks, rev)
			return AccessEvent{}, ErrAccessGrantUnavailable
		}
	}

	// Mark in-flight and release the mutex for the store write. A concurrent
	// Resolve now fails closed at the resolving check above instead of
	// double-promoting the same directory.
	i.resolving[event.ID] = struct{}{}
	workspaceID := event.WorkspaceID
	directory := event.Directory
	i.mu.Unlock()

	revision, err := i.grant.PromoteSandboxPath(workspaceID, access, directory)

	i.mu.Lock()
	delete(i.resolving, event.ID)
	if err != nil {
		i.mu.Unlock()
		return AccessEvent{}, err
	}
	// Re-find by ID: the slice may have shifted while the mutex was released.
	index = -1
	for n := range i.events {
		if i.events[n].ID == req.EventID {
			index = n
			break
		}
	}
	if index < 0 {
		// Dropped while promoting (the inbox overflowed). The grant is
		// durable; the event is gone with the lost counter already bumped.
		i.mu.Unlock()
		return AccessEvent{}, ErrAccessEventNotFound
	}
	event = &i.events[index]
	event.State = AccessStateGranted
	event.Decision = req.Decision
	event.ProfileRevision = revision
	i.revision++
	rev, callbacks := i.snapshotWatchersLocked()
	resolved := *event
	i.mu.Unlock()
	go notifyAccessWatchers(callbacks, rev)
	return resolved, nil
}

func (i *AccessInbox) CloseSession(identity SessionIdentity) {
	i.mu.Lock()
	changed := false
	for n := range i.events {
		event := &i.events[n]
		if event.SessionID == identity.SessionID && event.InstanceID == identity.InstanceID && event.SessionEpoch == identity.Epoch && event.State == AccessStatePending {
			event.State = AccessStateExpired
			changed = true
		}
	}
	if !changed {
		i.mu.Unlock()
		return
	}
	i.revision++
	rev, callbacks := i.snapshotWatchersLocked()
	i.mu.Unlock()
	notifyAccessWatchers(callbacks, rev)
}

func (i *AccessInbox) dropOldestLocked() {
	if len(i.events) == 0 {
		return
	}
	delete(i.grants, i.events[0].ID)
	i.events = append(i.events[:0], i.events[1:]...)
	i.lost++
}

func (i *AccessInbox) rebuildKeysLocked() {
	clear(i.keys)
	for n := range i.events {
		if i.events[n].State == AccessStatePending {
			i.keys[accessEventKeyFromEvent(i.events[n])] = n
		}
	}
}

func (i *AccessInbox) snapshotWatchersLocked() (uint64, []func(uint64)) {
	callbacks := make([]func(uint64), 0, len(i.watchers))
	for _, fn := range i.watchers {
		callbacks = append(callbacks, fn)
	}
	return i.revision, callbacks
}

func notifyAccessWatchers(callbacks []func(uint64), revision uint64) {
	for _, callback := range callbacks {
		callback(revision)
	}
}

func accessEventKey(observation AccessObservation) string {
	return observation.Identity.SessionID + "\x00" + observation.Identity.InstanceID + "\x00" + strconv.FormatUint(observation.Identity.Epoch, 10) + "\x00" + observation.Executable + "\x00" + observation.Path + "\x00" + string(observation.Access)
}

func accessEventKeyFromEvent(event AccessEvent) string {
	return event.SessionID + "\x00" + event.InstanceID + "\x00" + strconv.FormatUint(event.SessionEpoch, 10) + "\x00" + event.Executable + "\x00" + event.Path + "\x00" + string(event.Access)
}

func accessGrantDirectory(path string) (string, *accessGrantCheck) {
	candidate := path
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		candidate = filepath.Dir(candidate)
		info, err = os.Stat(candidate)
	}
	if err != nil || !info.IsDir() {
		return "", nil
	}
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil || !filepath.IsAbs(canonical) {
		return "", nil
	}
	canonical = filepath.Clean(canonical)
	if filepath.Dir(canonical) == canonical {
		return "", nil
	}
	return canonical, &accessGrantCheck{lexical: candidate, canonical: canonical, info: info}
}

func validAccessText(value string, maxBytes int, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	if len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func newAccessEventID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
