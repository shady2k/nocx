package transport

// Behavioral tests for the provider-side watch (nocx-oalo1.12.10): a binding
// whose provider watches for it emits files.changed from what the provider
// announces, runs no ladder of its own, and releases its watches while the
// panel is hidden so the far host stops polling.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/log/logtest"
	"github.com/shady2k/nocx/internal/operation/domains"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// fakeWatch is one provider-side subscription the test fires by hand. It is
// deliberately a POLLING kind: the transport's decision is Watch's answer, not
// the mode's, and the mode a helperfs watch reports is exactly this one.
type fakeWatch struct {
	path   string
	events chan struct{}
	owner  *watchingProvider

	mu     sync.Mutex
	closed bool
}

func (w *fakeWatch) Events() <-chan struct{} { return w.events }

func (w *fakeWatch) Mode() filesystem.WatchMode {
	return filesystem.WatchMode{Kind: filesystem.WatchPolling}
}

func (w *fakeWatch) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	w.owner.uninstall(w.path, w)
	close(w.events)
	return nil
}

func (w *fakeWatch) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// fire announces one change on this watch.
func (w *fakeWatch) fire() {
	select {
	case w.events <- struct{}{}:
	default:
	}
}

// watchingProvider is the local provider plus a watch seam: the transport sees
// a provider that can announce changes without being listed, which is what a
// helper-backed binding looks like from here.
type watchingProvider struct {
	*local.Provider

	mu          sync.Mutex
	watches     map[string]*fakeWatch
	lists       int
	listsByPath map[string]int
	pathErrors  map[string]error
	listErrors  map[string]error
	watchCalls  map[string]int
}

func newWatchingProvider(root string) *watchingProvider {
	return &watchingProvider{
		Provider: local.New(local.WithRoot(root)), watches: map[string]*fakeWatch{},
		listsByPath: map[string]int{}, pathErrors: map[string]error{},
		listErrors: map[string]error{}, watchCalls: map[string]int{},
	}
}

func (p *watchingProvider) Watch(_ context.Context, path string) (filesystem.Watch, error) {
	w := &fakeWatch{path: path, events: make(chan struct{}, 1), owner: p}
	p.mu.Lock()
	p.watchCalls[path]++
	if err := p.pathErrors[path]; err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if previous := p.watches[path]; previous != nil {
		p.mu.Unlock()
		return nil, errors.New("filesystem: watch already installed for " + path)
	}
	p.watches[path] = w
	p.mu.Unlock()
	return w, nil
}

func (p *watchingProvider) List(ctx context.Context, path string, page filesystem.Page) (filesystem.Listing, error) {
	p.mu.Lock()
	p.lists++
	p.listsByPath[path]++
	err := p.pathErrors[path]
	if err == nil {
		err = p.listErrors[path]
	}
	p.mu.Unlock()
	if err != nil {
		return filesystem.Listing{}, err
	}
	return p.Provider.List(ctx, path, page)
}

func (p *watchingProvider) refusePath(path string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pathErrors[path] = err
}

func (p *watchingProvider) refuseList(path string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listErrors[path] = err
}

func (p *watchingProvider) watchCount(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.watchCalls[path]
}

func (p *watchingProvider) listCount(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.listsByPath[path]
}

func (p *watchingProvider) Close() error { return nil }

func (p *watchingProvider) listsSoFar() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lists
}

// uninstall forgets a watch that was closed, so a released path can be watched
// again — the visible edge installs a new subscription where the hidden edge
// released the old one.
func (p *watchingProvider) uninstall(path string, w *fakeWatch) {
	p.mu.Lock()
	if p.watches[path] == w {
		delete(p.watches, path)
	}
	p.mu.Unlock()
}

// installedWatches answers the watches currently installed, by path.
func (p *watchingProvider) installedWatches() map[string]*fakeWatch {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]*fakeWatch, len(p.watches))
	for path, w := range p.watches {
		out[path] = w
	}
	return out
}

func (p *watchingProvider) watchFor(t *testing.T, path string) *fakeWatch {
	t.Helper()
	w := p.installedWatches()[path]
	if w == nil {
		t.Fatalf("no provider watch is installed for %s", path)
	}
	return w
}

func watchingFactory(p **watchingProvider) FilesystemProviderFactory {
	return func(_ session.Session, rootPath string) (filesystem.Provider, error) {
		provider := newWatchingProvider(rootPath)
		*p = provider
		return provider, nil
	}
}

// TestFilesWatch_ProviderWatchEmitsOneChangeAndRunsNoLadder is the leaf's
// acceptance line on the transport side: one announcement is one
// files.changed, a quiet folder sends nothing, and there is no timer listing
// directories behind the panel's back.
func TestFilesWatch_ProviderWatchEmitsOneChangeAndRunsNoLadder(t *testing.T) {
	var provider *watchingProvider
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)))
	dir := filesDir(t)
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, dir, 2)
	e.watchDir(t, bid, []string{dir}, 3)

	if got := provider.listsSoFar(); got != 1 {
		t.Fatalf("listings after files.watch = %d, want the single baseline", got)
	}
	w := provider.watchFor(t, dir)

	// The provider announces a change; the transport re-lists that one path
	// and emits exactly one files.changed carrying the new revision.
	changeDirFile(t, dir, "note.txt", "two")
	w.fire()
	params := readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
	if params["bindingId"] != bid || params["path"] != dir {
		t.Fatalf("files.changed = %v, want binding %q path %q", params, bid, dir)
	}
	if rev, _ := params["rev"].(string); rev == "" {
		t.Fatal("files.changed from a provider watch carried no revision")
	}
	if got := provider.listsSoFar(); got != 2 {
		t.Fatalf("listings after one announcement = %d, want 2 (baseline + one)", got)
	}

	// SILENCE, and this is the last thing the connection is asked: a quiet
	// folder announces nothing for four rungs of the transport's own ladder,
	// and the count says no timer replaced the provider's announcements.
	if got := drainFilesChanged(t, e.conn, 4*defaultFilesPollInterval); len(got) != 0 {
		t.Fatalf("a quiet provider-watched folder announced %v", got)
	}
	if got := provider.listsSoFar(); got != 2 {
		t.Fatalf("listings while quiet = %d, want 2 (no ladder)", got)
	}
}

// TestFilesWatch_HiddenPanelReleasesTheProviderWatch is the visibility half:
// while the panel is hidden the provider watch is closed — so the far host
// polls nothing — and the return installs a new one and catches the change that
// happened while nobody was listening.
func TestFilesWatch_HiddenPanelReleasesTheProviderWatch(t *testing.T) {
	var provider *watchingProvider
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)))
	dir := filesDir(t)
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, dir, 2)
	e.watchDir(t, bid, []string{dir}, 3)
	first := provider.watchFor(t, dir)

	jsonrpcCallWithID(t, e.conn, "files.visible", map[string]any{"bindingId": bid, "visible": false}, 4)
	waittest.WaitFor(t, "the hidden panel's provider watch to be released", func() bool { return first.isClosed() })
	waittest.WaitFor(t, "the transport to record the release", func() bool {
		e.ws.filesMu.Lock()
		w := e.ws.filesBindings[bid].watcher
		e.ws.filesMu.Unlock()
		w.mu.Lock()
		defer w.mu.Unlock()
		return !w.providerInstalled
	})
	baseline := provider.listsSoFar()

	// The change lands while the panel is hidden: nothing is polling, so
	// nothing can announce it — and the host's own poll ended with the watch
	// that was just released.
	changeDirFile(t, dir, "note.txt", "two")

	// The return installs a new watch and takes the catch-up listing, which
	// reports what happened while nobody was listening.
	jsonrpcCallWithID(t, e.conn, "files.visible", map[string]any{"bindingId": bid, "visible": true}, 5)
	waittest.WaitFor(t, "the visible panel to install a provider watch", func() bool {
		return len(provider.installedWatches()) == 1 && !provider.watchFor(t, dir).isClosed()
	})
	params := readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
	if params["path"] != dir {
		t.Fatalf("catch-up announced %v, want path %q", params, dir)
	}
	// Exactly one listing for the return: had the hidden panel kept polling,
	// the count would be higher than baseline+1.
	if got := provider.listsSoFar(); got != baseline+1 {
		t.Fatalf("the return listed %d times, want exactly the one catch-up", got-baseline)
	}

	// And the new subscription is live.
	second := provider.watchFor(t, dir)
	changeDirFile(t, dir, "note.txt", "three")
	second.fire()
	readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
}

// TestFilesClose_ReleasesTheProviderWatch is the binding-lifetime half: a
// closed binding's provider watch is closed with it, which is what tells the
// far host to stop polling a directory nobody is showing.
func TestFilesClose_ReleasesTheProviderWatch(t *testing.T) {
	var provider *watchingProvider
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)))
	dir := filesDir(t)
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, dir, 2)
	e.watchDir(t, bid, []string{dir}, 3)
	w := provider.watchFor(t, dir)

	resp := jsonrpcCallWithID(t, e.conn, "files.close", map[string]any{"bindingId": bid}, 4)
	var envelope struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("files.close: unmarshal: %v", err)
	}
	if envelope.Error != nil {
		t.Fatalf("files.close: %+v", envelope.Error)
	}
	waittest.WaitFor(t, "the closed binding's provider watch to be released", func() bool { return w.isClosed() })
}

// TestFilesWatch_LocalBindingKeepsItsLadder is the other half of the same
// decision: a provider that answers ErrWatchUnavailable changes nothing, and
// the transport keeps polling as it always has.
func TestFilesWatch_LocalBindingKeepsItsLadder(t *testing.T) {
	e := newFilesTestEnv(t)
	dir := filesDir(t)
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, dir, 2)
	w := e.watchDir(t, bid, []string{dir}, 3)
	if w.tryProvider {
		t.Fatal("a local binding was treated as provider-watched")
	}
	if w.signals != nil {
		t.Fatal("a local binding was given a provider change stream")
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The poll loop still notices it, on its own cadence.
	params := readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
	if params["path"] != dir {
		t.Fatalf("files.changed = %v, want path %q", params, dir)
	}
}

func filesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func changeDirFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readNotificationMap waits for one notification the shared inbox has not seen
// yet and answers its params decoded.
func readNotificationMap(t *testing.T, conn *websocket.Conn, method string, d time.Duration) map[string]any {
	t.Helper()
	raw := readNotification(t, conn, method, d)
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("decode %s params: %v", method, err)
	}
	return params
}

func TestFilesWatch_LocalBindingHiddenAtInstallKeepsItsLadder(t *testing.T) {
	e := newFilesTestEnv(t)
	dir := filesDir(t)
	changeDirFile(t, dir, "note.txt", "initial")
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, dir, 2)
	e.setVisible(t, bid, false, 3)
	e.watchDir(t, bid, []string{dir}, 4)
	e.setVisible(t, bid, true, 5)
	// The first change notification is the catch-up for the hidden interval.
	readNotificationMap(t, e.conn, "files.changed", 5*time.Second)

	changeDirFile(t, dir, "note.txt", "changed after opening")
	params := readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
	if params["path"] != dir {
		t.Fatalf("files.changed path = %v, want %q", params["path"], dir)
	}
}

func TestFilesWatch_ReplacingProviderWatchSetKeepsEvents(t *testing.T) {
	var provider *watchingProvider
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)))
	dir := filesDir(t)
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	changeDirFile(t, dir, "note.txt", "initial")
	changeDirFile(t, child, "note.txt", "initial")
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, dir, 2)
	e.watchDir(t, bid, []string{dir}, 3)
	changeDirFile(t, dir, "note.txt", "parent changed")
	provider.watchFor(t, dir).fire()
	readNotificationMap(t, e.conn, "files.changed", 5*time.Second)

	e.watchDir(t, bid, []string{dir, child}, 4)
	changeDirFile(t, child, "note.txt", "child changed")
	provider.watchFor(t, child).fire()
	params := readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
	if params["path"] != child {
		t.Fatalf("files.changed path = %v, want %q", params["path"], child)
	}
}

func TestFilesWatch_ForegroundListDoesNotPollQuietSibling(t *testing.T) {
	var provider *watchingProvider
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)))
	e.ws.filesPollInterval = 25 * time.Millisecond
	root := filesDir(t)
	quiet := filepath.Join(root, "quiet")
	if err := os.Mkdir(quiet, 0o700); err != nil {
		t.Fatal(err)
	}
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, root, 2)
	w := e.watchDir(t, bid, []string{root, quiet}, 3)
	waittest.WaitFor(t, "the quiet folder's transport polling deadline to pass", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return !w.paths[quiet].dueAt.After(time.Now())
	})
	baseline := provider.listCount(quiet)

	changeDirFile(t, root, "note.txt", "changed")
	provider.watchFor(t, root).fire()
	readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
	rootLists := provider.listCount(root)
	jsonrpcCallWithID(t, e.conn, "files.list", map[string]any{"bindingId": bid, "path": root, "limit": 1}, 4)
	if got := provider.listCount(root); got != rootLists+1 {
		t.Fatalf("foreground list issued %d lists of %q, want one", got-rootLists, root)
	}
	drainFilesChanged(t, e.conn, 150*time.Millisecond)
	if got := provider.listCount(quiet); got != baseline {
		t.Fatalf("foreground list of %q made %d extra lists of quiet folder %q", root, got-baseline, quiet)
	}
	w.requestPollNow(root)
	waittest.WaitFor(t, "the explicit refresh to list the requested folder", func() bool {
		return provider.listCount(root) == rootLists+2
	})
	if got := provider.listCount(quiet); got != baseline {
		t.Fatalf("explicit refresh of %q also listed quiet folder %q", root, quiet)
	}
}

func TestFilesWatch_VisibleAfterWatchRefusalKeepsHealthyFolderUpdated(t *testing.T) {
	var provider *watchingProvider
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)))
	e.ws.filesPollInterval = 25 * time.Millisecond
	healthy := filesDir(t)
	locked := filepath.Join(healthy, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, healthy, 2)
	e.watchDir(t, bid, []string{healthy, locked}, 3)
	e.setVisible(t, bid, false, 4)
	waittest.WaitFor(t, "the hidden panel to release its watches", func() bool {
		return len(provider.installedWatches()) == 0
	})
	provider.refusePath(locked, &filesystem.ErrPermission{Path: locked})
	changeDirFile(t, healthy, "note.txt", "changed while hidden")
	e.setVisible(t, bid, true, 5)
	readNotificationMap(t, e.conn, "files.changed", 5*time.Second)

	changeDirFile(t, healthy, "note.txt", "changed after the failed install")
	params := readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
	if params["path"] != healthy {
		t.Fatalf("files.changed path = %v, want %q", params["path"], healthy)
	}
	if got := provider.watchCount(locked); got != 2 {
		t.Fatalf("watch attempts after transport polls = %d, want the original and the reveal attempt", got)
	}
}

func TestFilesWatch_VisibleDuringCutRecoversWhenHelperReturns(t *testing.T) {
	var provider *watchingProvider
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)))
	e.ws.filesPollInterval = 25 * time.Millisecond
	e.ws.filesRefreshStateThreshold = 200 * time.Millisecond
	dir := filesDir(t)
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, dir, 2)
	e.watchDir(t, bid, []string{dir}, 3)
	e.setVisible(t, bid, false, 4)
	waittest.WaitFor(t, "the hidden panel to release its watch", func() bool {
		return len(provider.installedWatches()) == 0
	})
	provider.refusePath(dir, &filesystem.ErrUnavailable{Reason: "helper-lost"})
	e.setVisible(t, bid, true, 5)
	delayed := readNotificationMap(t, e.conn, "files.refreshStateChanged", 5*time.Second)
	if delayed["state"] != "delayed" {
		t.Fatalf("refresh state during the cut = %v, want delayed", delayed)
	}
	changeDirFile(t, dir, "note.txt", "changed during the cut")
	provider.refusePath(dir, nil)
	changed := readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
	if changed["path"] != dir {
		t.Fatalf("files.changed path = %v, want %q", changed["path"], dir)
	}
	fresh := readNotificationMap(t, e.conn, "files.refreshStateChanged", 5*time.Second)
	if fresh["state"] != "ok" {
		t.Fatalf("refresh state after recovery = %v, want ok", fresh)
	}
}

func TestFilesWatch_HiddenReleaseRetriesAfterDomainRecovers(t *testing.T) {
	logger := logtest.Slog(t)
	var provider *watchingProvider
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)), func(s *WSServer) {
		s.log = log.NewSlogAdapter(logger)
	})
	e.ws.filesDomains = domains.New(domains.Options{
		PerDomain: 2, PersonReserve: 1, AbandonedPerDomain: 1, AbandonedGlobal: 4, HealthyReserve: 1,
	})
	dir := filesDir(t)
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, dir, 2)
	w := e.watchDir(t, bid, []string{dir}, 3)
	installed := provider.watchFor(t, dir)
	baseline := provider.listsSoFar()

	domain := w.handle.DomainKey("")
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	work, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- e.ws.filesDomains.Do(domains.Request{
			Key: domains.Key{Kind: domain.Kind, ID: domain.ID}, Turn: t.Context(),
			Work: func() (context.Context, context.CancelFunc) { return work, cancel },
			Run: func(context.Context) error {
				close(entered)
				<-release
				return nil
			},
		})
	}()
	<-entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked domain work = %v, want context.Canceled", err)
	}
	e.setVisible(t, bid, false, 4)
	if !logtest.WaitForSlog(logger, 5*time.Second, func(record logtest.Record) bool {
		return record.Message == "files provider watch sync failed"
	}) {
		t.Fatal("hiding the panel did not attempt a release while the domain was suspect")
	}
	unblock()
	waittest.WaitFor(t, "the hidden panel to release its watch after domain recovery", installed.isClosed)
	if got := provider.listsSoFar(); got != baseline {
		t.Fatalf("the hidden panel issued %d extra lists during watch release", got-baseline)
	}
}

func TestFilesWatch_RetriesFailedSignalListings(t *testing.T) {
	for _, trigger := range []string{"provider signal", "reveal catch-up"} {
		t.Run(trigger, func(t *testing.T) {
			var provider *watchingProvider
			e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)))
			e.ws.filesPollInterval = 30 * time.Millisecond
			dir := filesDir(t)
			quiet := filepath.Join(dir, "quiet")
			if err := os.Mkdir(quiet, 0o700); err != nil {
				t.Fatal(err)
			}
			sid := e.openSession(t, 1)
			bid := e.openBinding(t, sid, dir, 2)
			w := e.watchDir(t, bid, []string{dir, quiet}, 3)
			lists, quietLists := provider.listCount(dir), provider.listCount(quiet)
			watch := provider.watchFor(t, dir)
			catchUp := trigger == "reveal catch-up"
			if catchUp {
				e.setVisible(t, bid, false, 4)
				waittest.WaitFor(t, "the hidden panel to release its watch", watch.isClosed)
				quietLists++
			}

			provider.refuseList(dir, &filesystem.ErrTimedOut{})
			changeDirFile(t, dir, "note.txt", "changed")
			if catchUp {
				e.setVisible(t, bid, true, 5)
			} else {
				watch.fire()
			}
			var retryDue time.Time
			waittest.WaitFor(t, "the failed listing to enter backoff", func() bool {
				w.mu.Lock()
				defer w.mu.Unlock()
				entry := w.paths[dir]
				if entry.failureStep == 0 {
					return false
				}
				retryDue = entry.dueAt
				return true
			})
			provider.refuseList(dir, nil)

			changed := readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
			if changed["path"] != dir || time.Now().Before(retryDue) {
				t.Fatalf("wanted a retry of %q after %s, got %v", dir, retryDue, changed)
			}
			if got := provider.listCount(dir); got != lists+2 {
				t.Fatalf("changed folder got %d extra listings, want two", got-lists)
			}
			if got := provider.listCount(quiet); got != quietLists {
				t.Fatalf("quiet folder got %d listings, want %d", got, quietLists)
			}
		})
	}
}

func TestFilesWatch_ForegroundListClearsPendingSignalRetry(t *testing.T) {
	var provider *watchingProvider
	e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)))
	e.ws.filesPollInterval = 100 * time.Millisecond
	dir := filesDir(t)
	sid := e.openSession(t, 1)
	bid := e.openBinding(t, sid, dir, 2)
	w := e.watchDir(t, bid, []string{dir}, 3)
	provider.refuseList(dir, &filesystem.ErrTimedOut{})
	changeDirFile(t, dir, "note.txt", "changed")
	provider.watchFor(t, dir).fire()
	var due time.Time
	waittest.WaitFor(t, "the failed listing to enter backoff", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		entry := w.paths[dir]
		if entry.failureStep == 0 {
			return false
		}
		due = entry.dueAt
		return true
	})
	provider.refuseList(dir, nil)

	raw := jsonrpcCallWithID(t, e.conn, "files.list", map[string]any{"bindingId": bid, "path": dir, "limit": 1}, 4)
	if state, _ := filesListState(t, raw); state != "ok" {
		t.Fatalf("foreground list state = %q, want ok", state)
	}
	before := provider.listCount(dir)
	drainFilesChanged(t, e.conn, time.Until(due)+250*time.Millisecond)
	if got := provider.listCount(dir); got != before {
		t.Fatalf("foreground read left %d redundant retry listings", got-before)
	}
}

func TestFilesWatch_RecoversProviderWatchOnExplicitEdge(t *testing.T) {
	for _, trigger := range []string{"hide and reveal", "repeat files.watch"} {
		t.Run(trigger, func(t *testing.T) {
			var provider *watchingProvider
			e := newFilesTestEnv(t, WithFilesystemProviderFactory(watchingFactory(&provider)))
			e.ws.filesPollInterval = 25 * time.Millisecond
			dir := filesDir(t)
			sid := e.openSession(t, 1)
			bid := e.openBinding(t, sid, dir, 2)
			w := e.watchDir(t, bid, []string{dir}, 3)
			original := provider.watchFor(t, dir)
			e.setVisible(t, bid, false, 4)
			waittest.WaitFor(t, "the hidden panel to release its watch", original.isClosed)
			provider.refusePath(dir, &filesystem.ErrUnavailable{Reason: "helper-lost"})
			e.setVisible(t, bid, true, 5)
			waittest.WaitFor(t, "the refused watch to fall back to polling", func() bool {
				w.binding.watchMu.Lock()
				defer w.binding.watchMu.Unlock()
				return w.pollingFallback
			})
			provider.refusePath(dir, nil)
			changeDirFile(t, dir, "note.txt", "seen by transport polling")
			readNotificationMap(t, e.conn, "files.changed", 5*time.Second)
			if got := provider.watchCount(dir); got != 2 {
				t.Fatalf("watch attempts without an explicit recovery edge = %d, want 2", got)
			}

			if trigger == "hide and reveal" {
				e.setVisible(t, bid, false, 6)
				e.setVisible(t, bid, true, 7)
			} else {
				e.watchDir(t, bid, []string{dir}, 6)
			}
			waittest.WaitFor(t, "the explicit edge to install a provider watch again", func() bool {
				return len(provider.installedWatches()) == 1
			})
			waittest.WaitFor(t, "the recovered signal watch to finish catch-up and reach its old deadline", func() bool {
				w.mu.Lock()
				defer w.mu.Unlock()
				entry := w.paths[dir]
				return w.catchUp[dir] == nil && entry.dueAt.After(entry.lastSuccessfulObservation) && !entry.dueAt.After(time.Now())
			})
			changeDirFile(t, dir, "note.txt", "seen through the restored provider watch")
			provider.watchFor(t, dir).fire()
			if got := readNotificationMap(t, e.conn, "files.changed", 5*time.Second)["path"]; got != dir {
				t.Fatalf("files.changed path = %v, want %q", got, dir)
			}
			before := provider.listCount(dir)
			drainFilesChanged(t, e.conn, 250*time.Millisecond)
			if got := provider.listCount(dir); got != before {
				t.Fatalf("transport listed the recovered provider watch %d extra times", got-before)
			}
		})
	}
}
