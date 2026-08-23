package commandnames

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// key is the cache key of one shared enumeration: the resolved route, the
// integration generation, the remote user, the shell family, and a hash of
// the effective PATH. Each component defends against a specific confusion,
// and each is asserted separately in the tests:
//
//   - route — two hosts must never share a name set;
//   - generation — a set computed by a different version of ourselves is a
//     different set, because the script that computed it changed;
//   - user — `root` and an ordinary user see different PATH directories and
//     different permissions inside them;
//   - shell family — the family decides what the session-local half is, and
//     serving one family's key to another would pair the wrong halves;
//   - PATH hash — a change to PATH is a different question, not an
//     invalidated answer, which is exactly why the mtime probe does not need
//     to notice it.
//
// It is a comparable struct rather than a joined string on purpose: a
// separator collision between two components would silently merge two
// entries, and there is no separator here to collide.
type key struct {
	route       string
	generation  string
	user        string
	shellFamily string
	pathHash    string
}

// entry is one cached shared enumeration.
type entry struct {
	names     []string
	truncated bool
	stamps    []DirStamp
	// stamped records whether the probe that produced these stamps could
	// stamp every directory. An entry built from unstampable directories is
	// never reported current: it is served as stale with its age, because
	// there is no event that can invalidate it and the age is the only
	// honest thing left to say about it.
	stamped   bool
	scannedAt time.Time
}

// flight is one in-progress scan. Every caller for the same key joins it, so
// eight tabs opened at once are one enumeration and not eight.
type flight struct {
	done chan struct{}
	res  Result
}

// Service is the backend-owned, in-memory command-name cache.
//
// It holds no file and writes nothing to disk: the cache dies with the
// application, which is a bound that costs nothing to maintain — there is no
// cross-restart state to invalidate, and nothing of the user's on our disk
// that a restart could leave stale.
type Service struct {
	now func() time.Time
	log log.Logger

	mu      sync.Mutex
	entries map[key]*entry
	// last is the most recent key per identity. It is what lets a session
	// whose PROBE failed still be served the snapshot that identity already
	// has: without the key there is nothing to look up, and throwing the
	// snapshot away because the probe blinked would turn one lost packet
	// into a lost cache.
	last    map[Identity]key
	flights map[key]*flight
}

// New builds the service. now is the time source (injected: no test here
// waits on a duration). logger may be nil.
func New(now func() time.Time, logger log.Logger) *Service {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = log.NewSlogAdapter(slog.New(slog.DiscardHandler))
	}
	return &Service{
		now:     now,
		log:     logger,
		entries: make(map[key]*entry),
		last:    make(map[Identity]key),
		flights: make(map[key]*flight),
	}
}

// Names answers one session. It probes (cheap, every session), decides
// whether the cached enumeration still describes the far side, and scans
// only when it does not.
//
// It never returns StateRunning: the call either produces a terminal state
// or joins the scan already in flight. "Running" is what the caller knows
// while this has not returned, and it is the only condition under which
// telling a user to wait is true.
func (s *Service) Names(ctx context.Context, src Source) Result {
	id := src.Identity()

	probe, err := src.Probe(ctx)
	if err != nil {
		// No key can be computed, so nothing can be looked up by it. Fall
		// back to the identity's last key, and serve that snapshot only
		// while it is inside the backstop.
		return s.serveLastOrFail(id, StateFailed, "command names unavailable: "+err.Error())
	}

	k := key{
		route:       id.Route,
		generation:  id.Generation,
		user:        probe.User,
		shellFamily: probe.ShellFamily,
		pathHash:    hashPath(probe.Path),
	}
	stamps := boundStamps(probe.Stamps)

	return s.serve(ctx, src, id, k, probe, stamps)
}

// StampsHeld reports how many directory stamps the identity's current entry
// holds. It exists so the probe's bound is checkable at the seam that
// enforces it rather than inferred from the script that produces it.
func (s *Service) StampsHeld(id Identity) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.last[id]
	if !ok {
		return 0
	}
	e, ok := s.entries[k]
	if !ok {
		return 0
	}
	return len(e.stamps)
}

// hitLocked answers from the cache when the entry still describes the far
// side: every stamp equal, and inside the backstop. A hit starts no scan —
// that is §11.35's second clause and the whole of the "ten tabs, one scan"
// claim. The caller holds s.mu, because the lookup and the decision to start
// a scan must be one atomic step (see serve).
func (s *Service) hitLocked(k key, stamps []DirStamp) (Result, bool) {
	e, ok := s.entries[k]
	if !ok {
		return Result{}, false
	}
	age := s.now().Sub(e.scannedAt)
	if age >= BackstopAge || !sameStamps(e.stamps, stamps) {
		return Result{}, false
	}
	if !e.stamped {
		return Result{State: StateStale, Names: e.names, Age: age, Truncated: e.truncated}, true
	}
	return Result{State: StateReady, Names: e.names, Truncated: e.truncated}, true
}

// serve answers from the cache, joins the scan already in flight, or starts
// the one scan for this key.
//
// The three decisions are made under ONE hold of s.mu, and that is the whole
// point of the function existing. Checking the cache, releasing the lock and
// then deciding to scan leaves a window: a caller that missed while the
// leader was still scanning can reach the flight map after the leader has
// finished and removed its flight, find nothing there, and start a second
// enumeration of a host that was just enumerated. It is a small window and it
// is real — the concurrency test found it on the first run that raced eight
// callers through a gated scan.
func (s *Service) serve(ctx context.Context, src Source, id Identity, k key, probe Probe, stamps []DirStamp) Result {
	s.mu.Lock()
	if res, ok := s.hitLocked(k, stamps); ok {
		s.mu.Unlock()
		return res
	}
	if f, running := s.flights[k]; running {
		s.mu.Unlock()
		<-f.done
		return f.res
	}
	f := &flight{done: make(chan struct{})}
	s.flights[k] = f
	s.mu.Unlock()

	res := s.runScan(ctx, src, id, k, probe, stamps)

	// The result is published to the joiners BEFORE the flight is removed:
	// a caller that finds the flight still registered waits on `done` and
	// reads `res`, and one that finds it gone has already seen the entry
	// runScan wrote under the same mutex. Removing it first would leave an
	// instant in which neither answer is available and a second scan looks
	// necessary.
	f.res = res
	close(f.done)
	s.mu.Lock()
	delete(s.flights, k)
	s.mu.Unlock()
	return res
}

func (s *Service) runScan(ctx context.Context, src Source, id Identity, k key, probe Probe, stamps []DirStamp) Result {
	scanned, err := src.Scan(ctx, probe)
	if err != nil {
		state := StateFailed
		reason := "command names could not be listed: " + err.Error()
		if errors.Is(err, ErrScanDeadline) {
			state = StateTimedOut
			reason = "the command-name scan did not finish inside its deadline"
		}
		// Nothing partial is published: a scan that did not complete leaves
		// the cache exactly as it was, so the next session scans again
		// rather than being served half an enumeration as though it were
		// the answer (§11.35, first clause).
		s.log.Debug("commandnames: scan did not complete", "route", id.Route, "state", string(state), "err", err)
		return s.serveLastOrFail(id, state, reason)
	}

	names, truncated := boundNames(scanned.Names)
	truncated = truncated || scanned.Truncated

	s.mu.Lock()
	s.entries[k] = &entry{
		names: names, truncated: truncated,
		stamps: stamps, stamped: probe.Stamped,
		scannedAt: s.now(),
	}
	s.last[id] = k
	s.mu.Unlock()

	return Result{State: StateReady, Names: names, Truncated: truncated}
}

// serveLastOrFail is the one place a degrade is decided. Inside the backstop
// the identity's last snapshot is served as `stale`, WITH its age; past it
// the snapshot is not claimed at all and the failure's own state stands.
//
// The age travels with the names deliberately: a stale set served without
// one is indistinguishable from a current one, which is the same lie the
// "still loading" row was telling in a different direction.
func (s *Service) serveLastOrFail(id Identity, state State, reason string) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.last[id]
	if ok {
		if e, held := s.entries[k]; held {
			age := s.now().Sub(e.scannedAt)
			if age < BackstopAge {
				return Result{State: StateStale, Names: e.names, Age: age, Truncated: e.truncated}
			}
			// Past the backstop it is no longer ours to offer. Drop it so
			// the memory goes too — the cache is bounded by the application's
			// life, and an entry nobody may serve is not part of that bound.
			delete(s.entries, k)
			delete(s.last, id)
		}
	}
	return Result{State: state, Reason: reason}
}

// hashPath hashes the effective PATH into the key. The PATH itself is not
// stored: it is a user's directory layout, it can be long, and equality is
// the only question ever asked of it.
func hashPath(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])
}

// boundStamps keeps at most MaxPathDirs stamps. The service keeps only what
// it compares: holding a 33rd stamp it will never look at would suggest an
// invalidation guarantee the bound does not give.
func boundStamps(in []DirStamp) []DirStamp {
	if len(in) > MaxPathDirs {
		in = in[:MaxPathDirs]
	}
	out := make([]DirStamp, len(in))
	copy(out, in)
	return out
}

func sameStamps(a, b []DirStamp) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// boundNames sorts, deduplicates and cuts the name set to the shared bounds.
// Sorting here rather than on the far side is two fewer processes on the
// path a fresh tab waits for, and it makes the byte bound decidable: names
// are counted in the encoded form the wire carries, one separator each.
func boundNames(in []string) ([]string, bool) {
	sort.Strings(in)
	out := make([]string, 0, len(in))
	bytes := 0
	truncated := false
	var prev string
	for i, n := range in {
		if n == "" {
			continue
		}
		if i > 0 && n == prev {
			continue
		}
		prev = n
		if len(out) >= MaxSharedNames || bytes+len(n)+1 > MaxSharedBytes {
			truncated = true
			break
		}
		out = append(out, n)
		bytes += len(n) + 1
	}
	return out, truncated
}
