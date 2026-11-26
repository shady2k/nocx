package helperfs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/helperfs"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
)

func sameResult[T any](t *testing.T, label string, want T, wantErr error, got T, gotErr error) {
	t.Helper()
	if wantErr != nil || gotErr != nil || !reflect.DeepEqual(want, got) {
		t.Fatalf("%s: local = %+v (%v), helper = %+v (%v)", label, want, wantErr, got, gotErr)
	}
}

func TestHelperFilesystemRoundTrip(t *testing.T) {
	base := tempTree(t)
	dir := filepath.Join(base, "real")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "note"), []byte("text beyond the preview"))
	link := filepath.Join(base, "linked")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	realFile, linkedFile := filepath.Join(dir, "note"), filepath.Join(link, "note")
	reference := local.New(local.WithRoot(link))
	svc := &fsObserver{Service: newHostService(t, local.New(local.WithRoot(base)))}
	reg, _ := singlePeer(t, svc)
	p := helperfs.New(reg, panelID, link)
	ctx := testContext(t)

	wantRoot, wantErr := reference.Root(ctx)
	root, err := p.Root(ctx)
	sameResult(t, "Root", wantRoot, wantErr, root, err)
	wantStat, wantErr := reference.Stat(ctx, linkedFile)
	stat, err := p.Stat(ctx, linkedFile)
	sameResult(t, "Stat", wantStat, wantErr, stat, err)

	before := len(svc.observed())
	page := filesystem.Page{Limit: 1}
	wantList, wantErr := reference.List(ctx, link, page)
	list, err := p.List(ctx, link, page)
	sameResult(t, "List", wantList, wantErr, list, err)
	calls := svc.observed()[before:]
	if len(calls) != 1 || calls[0].op != proto.OpList {
		t.Fatalf("List sent %v, want exactly one %s RPC", calls, proto.OpList)
	}
	t.Logf("List RPC count: %d", len(calls))
	if len(list.Entries) != 1 || list.Canonical != dir || list.Entries[0].Path != linkedFile {
		t.Fatalf("List lost canonical/lexical separation: %+v", list)
	}
	wantPath, wantErr := reference.Canonical(ctx, linkedFile)
	path, err := p.Canonical(ctx, linkedFile)
	sameResult(t, "Canonical", wantPath, wantErr, path, err)
	for _, limit := range []int64{5, 64} {
		wantRead, wantErr := reference.Read(ctx, linkedFile, limit)
		content, err := p.Read(ctx, linkedFile, limit)
		sameResult(t, "Read", wantRead, wantErr, content, err)
		if content.Path != linkedFile || content.Canonical != realFile {
			t.Fatalf("Read lost canonical/lexical separation: %+v", content)
		}
	}
	if _, err := p.Watch(ctx, link); !errors.As(err, new(*filesystem.ErrWatchUnavailable)) {
		t.Fatalf("Watch: %v, want ErrWatchUnavailable", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := helperfs.New(reg, panelID, link).Stat(ctx, linkedFile); err != nil {
		t.Fatalf("another binding lost the registry-owned client after Close: %v", err)
	}
}

func TestHelperFilesystemTypedRefusals(t *testing.T) {
	root := tempTree(t)
	file := filepath.Join(root, "note")
	writeFile(t, file, []byte("test"))
	writeFile(t, filepath.Join(root, "second"), nil)
	svc := &fsObserver{Service: newHostService(t, local.New(local.WithRoot(root), local.WithEntryCap(1)))}
	reg, _ := singlePeer(t, svc)
	p := helperfs.New(reg, panelID, root)
	ctx := testContext(t)

	missing := filepath.Join(root, "missing")
	_, err := p.Stat(ctx, missing)
	var notFound *filesystem.ErrNotFound
	if !errors.As(err, &notFound) || notFound.Path != missing || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(missing) = %v, want typed not-found and fs.ErrNotExist", err)
	}
	_, err = p.Source().Open(missing)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Source.Open(missing) = %v, want fs.ErrNotExist", err)
	}
	_, err = p.Stat(ctx, "relative/path")
	var invalid *filesystem.ErrInvalidPath
	if !errors.As(err, &invalid) || invalid.Reason == "" {
		t.Fatalf("Stat(relative path) = %v, want typed invalid-path with reason", err)
	}
	_, err = p.List(ctx, root, filesystem.Page{Offset: -1, Limit: 3})
	var page *filesystem.ErrInvalidPage
	if !errors.As(err, &page) || page.Offset != -1 || page.Limit != 3 {
		t.Fatalf("List(invalid page) = %v, want typed invalid-page with offset and limit", err)
	}
	_, err = p.List(ctx, file, filesystem.Page{Limit: 3})
	if !errors.As(err, new(*filesystem.ErrNotDir)) {
		t.Fatalf("List(file) = %v, want typed not-dir", err)
	}
	_, err = p.List(ctx, root, filesystem.Page{Limit: 1})
	var tooLarge *filesystem.ErrTooLarge
	if !errors.As(err, &tooLarge) || tooLarge.Limit != 1 || tooLarge.ObservedCount != 2 {
		t.Fatalf("List(over cap) = %v, want typed too-large with limit 1 and observed 2", err)
	}
	_, err = p.Read(ctx, root, 128)
	var notRegular *filesystem.ErrNotRegular
	if !errors.As(err, &notRegular) || notRegular.Kind != filesystem.KindDir {
		t.Fatalf("Read(directory) = %v, want typed not-regular with directory kind", err)
	}
	if got, err := p.Stat(ctx, file); err != nil || got.Kind != filesystem.KindRegular {
		t.Fatalf("the same lane after refusals: Stat = %+v (%v)", got, err)
	}
}

func TestHelperFilesystemConsentRevokesHeldList(t *testing.T) {
	root := tempTree(t)
	entered := make(chan struct{})
	svc := &fsObserver{Service: newHostService(t, local.New(local.WithRoot(root))),
		intercept: func(ctx context.Context, op string, _ json.RawMessage, next func() (any, error)) (any, error) {
			if op != proto.OpList {
				return next()
			}
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
	reg, consent := singlePeer(t, svc)
	p := helperfs.New(reg, panelID, root)
	ctx := testContext(t)
	result := make(chan error, 1)
	go func() { _, err := p.List(ctx, root, filesystem.Page{Limit: 10}); result <- err }()
	receive(t, entered)
	consent.deny()
	var unavailable *filesystem.ErrUnavailable
	if err := receive(t, result); !errors.As(err, &unavailable) || unavailable.Reason != "consent-denied" {
		t.Fatalf("List after consent denial = %v, want ErrUnavailable(consent-denied)", err)
	}
}

func TestHelperFilesystemBindingNeverMovesToAnotherConnection(t *testing.T) {
	rootA, rootB := tempTree(t), tempTree(t)
	serviceA := &fsObserver{Service: newHostService(t, local.New(local.WithRoot(rootA)))}
	serviceB := &fsObserver{Service: newHostService(t, local.New(local.WithRoot(rootB)))}
	a, _ := servePeer(t, serviceA, "host-a")
	b, _ := servePeer(t, serviceB, "host-b")
	const otherID = "other-connection"
	reg, conns, _ := registry(t, nil, map[string][]*client.Client{panelID: {a}, otherID: {b}})
	p := helperfs.New(reg, panelID, "")
	ctx := testContext(t)
	if r, err := p.Root(ctx); err != nil || r.Path != rootA {
		t.Fatalf("original binding root = %+v (%v), want %q", r, err, rootA)
	}
	gone := panelConnection(panelID)
	gone.State = hostconn.Ended
	conns.change(gone)
	conns.change(panelConnection(otherID))
	if _, err := reg.Acquire(ctx, otherID); err != nil {
		t.Fatalf("the replacement connection never reached serving: %v", err)
	}
	var unavailable *filesystem.ErrUnavailable
	if _, err := p.Root(ctx); !errors.As(err, &unavailable) || unavailable.Reason != "connection" {
		t.Fatalf("original binding after removal: %v, want ErrUnavailable(connection)", err)
	}
	if calls := serviceB.observed(); len(calls) != 0 {
		t.Fatalf("original binding sent file RPCs to the replacement host: %v", calls)
	}
}
