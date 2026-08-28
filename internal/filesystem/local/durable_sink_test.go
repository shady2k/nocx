package local

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/transfer"
)

func TestDurableSink_CancelPreservesExistingDestinationAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chosen.bin")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().DurableSink().Put(ctx, transfer.Upload{
		DestDir:  dir,
		Name:     "chosen.bin",
		Size:     7,
		OnExists: transfer.Overwrite,
	}, bytes.NewReader([]byte("replace")), nil)
	if err == nil {
		t.Fatal("cancelled durable write succeeded")
	}
	got, readErr := os.ReadFile(path) //nolint:gosec // path is under the test's own temporary directory
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "existing" {
		t.Fatalf("destination = %q, want existing bytes", got)
	}
	entries, readDirErr := os.ReadDir(dir)
	if readDirErr != nil {
		t.Fatal(readDirErr)
	}
	if len(entries) != 1 || entries[0].Name() != "chosen.bin" {
		t.Fatalf("temporary residue = %#v", entries)
	}
}

func TestDurableSink_MultiChunkBytesPromoteExactly(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte("bounded-chunk"), transfer.DefaultChunk/4)
	outcome, err := New().DurableSink().Put(context.Background(), transfer.Upload{
		DestDir:  dir,
		Name:     "chosen.bin",
		Size:     int64(len(body)),
		OnExists: transfer.Overwrite,
	}, bytes.NewReader(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.FinalName != "chosen.bin" {
		t.Fatalf("final name = %q, want chosen.bin", outcome.FinalName)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, outcome.FinalName)) //nolint:gosec // both components are test-owned
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("bytes differ: got %d want %d", len(got), len(body))
	}
}
