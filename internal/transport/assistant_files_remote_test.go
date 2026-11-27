package transport

// The acceptance test stage 10 exists for (nocx-a62cl.11.4): on a pane whose
// files live on another machine, the assistant's `files.*` tools read, list,
// search and CHANGE that machine's files, and this machine's same-named file is
// not touched at all.
//
// # Why the same absolute path on both sides
//
// The fixture gives the fake host a file at the SAME absolute path as a real
// file under t.TempDir, with different bytes. That is the whole proof: an
// implementation that fell back to this machine's disk — the defect the stage
// was planned around, where approval evidence was an os.Stat of the model's own
// path — would read "local bytes" and pass every assertion that only asks for
// "the file was read". Nothing here hands the tools a path they could not have
// reached both ways.
//
// # What is real
//
// The session registry, the filesystem registry, the provider factory, the two
// capability operations and the assistant's own binding source (filesFor) are
// the shipped ones; only the far provider is in memory, because the subject is
// the ROUTE and not the helper protocol (helperfs and hostsvc have their own
// fence tests, and e2e/assistant-ssh-files.spec.ts drives the shipped helper).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/control"
)

// TestAssistantFileToolsActOnThePanesHostAndNeverThisMachinesDisk is the
// stage's first criterion at the transport seam: each of the four tools acts on
// the far provider's bytes, and the local file with the same name keeps its
// bytes and its modification time.
func TestAssistantFileToolsActOnThePanesHostAndNeverThisMachinesDisk(t *testing.T) {
	root := t.TempDir() // the SAME absolute spelling on both machines
	const localBody = "local bytes: needle-local\n"
	permitPath := filepath.Join(root, "permit.conf")
	if err := os.WriteFile(permitPath, []byte(localBody), 0o600); err != nil {
		t.Fatalf("write the local same-named file: %v", err)
	}
	before := statOf(t, permitPath)

	far := newRemotePaneFiles(map[string]string{
		permitPath:                         "remote permit: needle-remote\n",
		filepath.Join(root, "ask.conf"):    "remote ask\n",
		filepath.Join(root, "src", "a.go"): "package a // needle-remote\n",
	})

	route, sess := remoteFilesRoute(t, far)
	source := route.filesFor(sess.ID())

	grant := autonomousGrantForTests(t, root)
	owner, _, err := assistant.NewClientAndRegistry(nil, content.Floor{})
	if err != nil {
		t.Fatalf("assistant registry: %v", err)
	}
	watched := 0
	bound, err := assistant.BindRunTools(context.Background(), owner, assistant.AskParams{
		Grant:      &grant,
		FreshGrant: func() *content.Grant { return &grant },
		Files:      source,
		// The assistant's own view of the run: one session, one attempt, one
		// turn — the facts the transport binds in ws_agent.go.
		KnownMaterial: remoteTestKnownMaterial{},
		// Every tool call has a durable attempt before it runs (design §6.4);
		// this fixture's ledger is the suite's own deliberately dumb one.
		AttemptLedger: &deliveryLedger{},
		RunID:         "run-remote-files",
		SessionID:     string(sess.ID()),
		Attempt:       1,
		TurnEntryID:   "turn-remote-files",
	}, func(assistant.ToolCall) error { watched++; return nil })
	if err != nil {
		t.Fatalf("BindRunTools: %v", err)
	}

	// 1. files.read sees the HOST's bytes.
	out := dispatchAssistantTool(t, bound, "files.read", "call-read", fmt.Sprintf(`{"path":%q}`, permitPath))
	var readResult struct {
		Revision string `json:"revision"`
		Text     string `json:"text"`
	}
	decodeJSON(t, out, &readResult)
	if !strings.Contains(out, "remote permit: needle-remote") {
		t.Fatalf("files.read = %q, want the host's bytes", out)
	}
	if strings.Contains(out, "needle-local") {
		t.Fatalf("files.read = %q, want nothing of this machine's copy", out)
	}
	if readResult.Revision == "" {
		t.Fatalf("files.read = %q, want a revision for the next call", out)
	}

	// 2. files.list sees the HOST's names — and not a file only this machine
	// has, which is the shortest statement of "the listing came from there".
	localOnly := filepath.Join(root, "local-only.txt")
	if err := os.WriteFile(localOnly, []byte("only here\n"), 0o600); err != nil {
		t.Fatalf("write the local-only file: %v", err)
	}
	listOut := dispatchAssistantTool(t, bound, "files.list", "call-list", fmt.Sprintf(`{"path":%q}`, root))
	for _, want := range []string{"permit.conf", "ask.conf"} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("files.list = %q, want the host's %q", listOut, want)
		}
	}
	if strings.Contains(listOut, "local-only.txt") {
		t.Fatalf("files.list = %q, want no row this machine alone has — the listing came from the local disk", listOut)
	}

	// 3. files.search matches the HOST's contents only.
	searchOut := dispatchAssistantTool(t, bound, "files.search", "call-search", fmt.Sprintf(`{"path":%q,"pattern":"needle"}`, root))
	if !strings.Contains(searchOut, "needle-remote") {
		t.Fatalf("files.search = %q, want the host's matching lines", searchOut)
	}
	if strings.Contains(searchOut, "needle-local") {
		t.Fatalf("files.search = %q, want no line of this machine's copy", searchOut)
	}

	// 4. files.edit changes the HOST's file, and nothing on this machine.
	editArgs := fmt.Sprintf(`{"path":%q,"revision":%q,"patch":"PUT 1.=1:\n+remote permit: needle-remote, edited"}`, permitPath, readResult.Revision)
	editOut := dispatchAssistantTool(t, bound, "files.edit", "call-edit", editArgs)
	if !strings.Contains(editOut, `"status":"applied"`) {
		t.Fatalf("files.edit = %q, want it applied on the host", editOut)
	}
	if got := far.body(permitPath); !strings.Contains(got, "edited") {
		t.Fatalf("host %s = %q, want the patch applied there", permitPath, got)
	}
	// The local same-named file: same bytes, same modification time.
	if after := statOf(t, permitPath); after != before {
		t.Fatalf("local %s = %+v, want it untouched (%+v)", permitPath, after, before)
	}
	// And the local-only file this test wrote is still there, so the
	// assertions above are about the host and not about a deleted fixture.
	if _, err := os.Stat(localOnly); err != nil {
		t.Fatalf("local-only fixture vanished: %v", err)
	}

	if watched != 4 {
		t.Fatalf("announced tool calls = %d, want the four that ran", watched)
	}
	// One binding per call, and none left behind: the run holds the registry
	// nowhere between calls.
	if got := far.closedCount(); got != 4 {
		t.Fatalf("provider closes = %d, want one per call", got)
	}
	if err := route.reg.CloseSession(sess.ID()); err != nil {
		t.Fatalf("CloseSession after the calls: %v", err)
	}
}

// remoteFilesRoute is the transport half of the fixture: a real session
// registry with one ADOPTED remote session, a real filesystem registry, and the
// assistant's route over them — the same two operations files.open runs on.
func remoteFilesRoute(t *testing.T, far *remotePaneFiles) (remoteFilesHandler, session.Session) {
	t.Helper()
	sessions := session.New(log.NewSlogAdapter(nil), &exitFakePTYFactory{fake: newExitFakePTY()})
	sess, err := sessions.Adopt(context.Background(), session.Config{
		Kind: session.KindRemote,
		Host: "prod.example",
		Cols: 80,
		Rows: 24,
	}, session.NewID(), pty.NewStub(log.NewSlogAdapter(nil)))
	if err != nil {
		t.Fatalf("adopt a remote session: %v", err)
	}
	t.Cleanup(func() { _ = sessions.Close(sess.ID()) })
	reg := filesystem.New()
	t.Cleanup(func() { _ = reg.CloseSession(sess.ID()) })
	factory := func(got session.Session, rootPath string) (filesystem.Provider, error) {
		if got.ID() != sess.ID() {
			return nil, fmt.Errorf("factory asked for session %q, want the run's %q", got.ID(), sess.ID())
		}
		if rootPath != "" {
			return nil, fmt.Errorf("factory asked for a root override %q, want the pane's own", rootPath)
		}
		return far, nil
	}
	handler := agentHandlers{
		filesOpen: capability.NewFilesystemOpenOperation(control.Unrestricted(), sessions, factory, reg),
		filesBind: capability.NewFilesystemBindingOperation(reg),
	}
	return remoteFilesHandler{agentHandlers: handler, reg: reg}, sess
}

// remoteFilesHandler is the handler plus the registry, so the test can close
// the session's bindings and assert nothing is left.
type remoteFilesHandler struct {
	agentHandlers
	reg *filesystem.Registry
}

func autonomousGrantForTests(t *testing.T, root string) content.Grant {
	t.Helper()
	policy := autonomousMatrixForTests()
	policy.Observe.Scopes = []content.GrantScope{{Kind: content.ResourcePath, ID: root}}
	policy.MutateReversible.Scopes = []content.GrantScope{{Kind: content.ResourcePath, ID: root}}
	return policy.AsGrant([]content.GrantScope{
		{Kind: content.ResourceSession, ID: "session-remote"},
		{Kind: content.ResourcePath, ID: root},
	})
}

func dispatchAssistantTool(t *testing.T, bound *assistant.RunTools, method, callID, args string) string {
	t.Helper()
	invocation, err := bound.Admit(context.Background())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	invocation.Context = context.Background()
	invocation.Method, invocation.RawParams = method, []byte(args)
	out, err := bound.Dispatch(invocation)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

func decodeJSON(t *testing.T, raw string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), into); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
}

// statOf is what "this file was not touched" means: its length and the moment
// it was last written.
type fileStateAt struct {
	size    int64
	mtimeNs int64
}

func statOf(t *testing.T, path string) fileStateAt {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fileStateAt{size: info.Size(), mtimeNs: info.ModTime().UnixNano()}
}

// remotePaneFiles is an in-memory filesystem standing in for the pane's host:
// absolute paths, the versions a conditional write fences on, and a counter of
// what was asked of it.
type remotePaneFiles struct {
	mu     sync.Mutex
	files  map[string]string
	mtime  map[string]int64
	clock  int64
	closes int
}

func newRemotePaneFiles(files map[string]string) *remotePaneFiles {
	p := &remotePaneFiles{files: map[string]string{}, mtime: map[string]int64{}}
	for path, body := range files {
		p.files[path] = body
		p.clock++
		p.mtime[path] = p.clock
	}
	return p
}

func (p *remotePaneFiles) body(path string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.files[path]
}

func (p *remotePaneFiles) closedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closes
}

func (p *remotePaneFiles) Root(context.Context) (filesystem.Root, error) {
	return filesystem.Root{Path: "/", Display: "~"}, nil
}

func (p *remotePaneFiles) Canonical(_ context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", &filesystem.ErrInvalidPath{Path: path, Reason: "path is not absolute"}
	}
	if filepath.Clean(path) != path {
		return "", &filesystem.ErrInvalidPath{Path: path, Reason: "path is not clean"}
	}
	return path, nil
}

func (p *remotePaneFiles) Stat(_ context.Context, path string) (filesystem.Stat, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.files[path]; ok {
		return filesystem.Stat{Kind: filesystem.KindRegular}, nil
	}
	if p.isDirLocked(path) {
		return filesystem.Stat{Kind: filesystem.KindDir}, nil
	}
	return filesystem.Stat{}, &filesystem.ErrNotFound{Path: path}
}

func (p *remotePaneFiles) isDirLocked(path string) bool {
	prefix := path
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	for file := range p.files {
		if strings.HasPrefix(file, prefix) {
			return true
		}
	}
	return false
}

func (p *remotePaneFiles) List(_ context.Context, path string, page filesystem.Page) (filesystem.Listing, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	prefix := path
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	entries := make([]filesystem.Entry, 0, len(p.files))
	seenDirs := map[string]bool{}
	for file := range p.files {
		if !strings.HasPrefix(file, prefix) {
			continue
		}
		rest := strings.TrimPrefix(file, prefix)
		name, deeper, isDir := strings.Cut(rest, "/")
		if isDir {
			if seenDirs[name] {
				continue
			}
			seenDirs[name] = true
			entries = append(entries, filesystem.Entry{
				Name: name,
				Path: filepath.Join(path, name),
				Kind: filesystem.KindDir,
			})
			continue
		}
		_ = deeper
		entries = append(entries, filesystem.Entry{
			Name:    name,
			Path:    file,
			Kind:    filesystem.KindRegular,
			Size:    int64(len(p.files[file])),
			ModTime: time.Unix(0, p.mtime[file]),
		})
	}
	if len(entries) == 0 {
		return filesystem.Listing{}, &filesystem.ErrNotFound{Path: path}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	offset := page.Offset
	if offset > len(entries) {
		offset = len(entries)
	}
	end := len(entries)
	if page.Limit > 0 && offset+page.Limit < end {
		end = offset + page.Limit
	}
	return filesystem.Listing{
		Path:      path,
		Canonical: path,
		Entries:   entries[offset:end],
		Offset:    offset,
		Total:     len(entries),
		HasMore:   end < len(entries),
	}, nil
}

func (p *remotePaneFiles) Read(_ context.Context, path string, maxBytes int64) (filesystem.Content, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	body, ok := p.files[path]
	if !ok {
		return filesystem.Content{}, &filesystem.ErrNotFound{Path: path}
	}
	limit := filesystem.ReadLimit(maxBytes)
	data := []byte(body)
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	return filesystem.PreviewContent(path, path, data, time.Unix(0, p.mtime[path]), truncated, false), nil
}

func (p *remotePaneFiles) Watch(context.Context, string) (filesystem.Watch, error) {
	return nil, errors.New("remotePaneFiles: no watches in this fixture")
}

func (p *remotePaneFiles) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closes++
	return nil
}

// Versioned is the conditional write the edit path fences on, exactly as
// helperfs provides it: a read that reports the bytes' version, and a commit
// that installs only if the version (or the absence) still holds.
func (p *remotePaneFiles) Versioned() filesystem.VersionedFiles { return p }

func (p *remotePaneFiles) ReadVersion(_ context.Context, path string, maxBytes int64) ([]byte, filesystem.Version, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	body, ok := p.files[path]
	if !ok {
		return nil, filesystem.Version{}, &filesystem.ErrNotFound{Path: path}
	}
	if int64(len(body)) > filesystem.ReadLimit(maxBytes) {
		return nil, filesystem.Version{}, &filesystem.ErrByteLimit{
			Path: path, Observed: int64(len(body)), Limit: filesystem.ReadLimit(maxBytes),
		}
	}
	return []byte(body), filesystem.Version{
		Size:    int64(len(body)),
		MtimeNs: p.mtime[path],
		SHA256:  filesystem.ContentDigest([]byte(body)),
	}, nil
}

func (p *remotePaneFiles) Commit(_ context.Context, path string, data []byte, expect filesystem.Expect) (filesystem.Version, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, exists := p.files[path]
	if expect.Absent {
		if exists {
			return filesystem.Version{}, &filesystem.ErrExists{Path: path, Err: os.ErrExist}
		}
	} else if expect.Version != nil {
		want := *expect.Version
		if !exists {
			return filesystem.Version{}, &filesystem.ErrChangedSinceRead{Path: path, Reason: "the file is gone"}
		}
		if int64(len(current)) != want.Size || p.mtime[path] != want.MtimeNs ||
			filesystem.ContentDigest([]byte(current)) != want.SHA256 {
			return filesystem.Version{}, &filesystem.ErrChangedSinceRead{Path: path, Reason: "the far file changed since it was read"}
		}
	}
	p.clock++
	p.files[path] = string(data)
	p.mtime[path] = p.clock
	return filesystem.Version{
		Size:    int64(len(data)),
		MtimeNs: p.clock,
		SHA256:  filesystem.ContentDigest(data),
	}, nil
}

// remoteTestKnownMaterial is the egress vault comparison for a run with no
// vault: it finds nothing, which is what a test about file routing needs.
type remoteTestKnownMaterial struct{}

func (remoteTestKnownMaterial) FindKnown(context.Context, string) ([]assistant.KnownMatch, error) {
	return nil, nil
}
