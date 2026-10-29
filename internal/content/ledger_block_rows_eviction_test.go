package content

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/log/logtest"
)

const evictionArtifact = "00000000-0000-7000-8000-00000000e001"

func newBlockRowsEvictionStore(t *testing.T) (*sqliteContent, string) {
	t.Helper()
	ctx, logger := logtest.New(t)
	db, err := Open(ctx, Config{
		Path: filepath.Join(t.TempDir(), "content.db"), Key: testKeyInternal(),
		Budget: testBudgetInternal(), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, ok := db.(*sqliteContent)
	if !ok {
		t.Fatalf("Open returned %T, want *sqliteContent", db)
	}
	entry, err := s.RecordCompleted(ctx, CompletedCommand{
		Client: "test-client", Env: Environment{ID: "local", Kind: EnvLocal},
		Cwd: "/repo", Intent: "eviction fixture", Source: SourceUser, Status: EntrySuccess,
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := s.OpenBlockOutput(ctx, OpenBlockOutput{EntryID: entry, ArtifactID: evictionArtifact}); err != nil || opened != evictionArtifact {
		t.Fatalf("open = %q, %v", opened, err)
	}
	return s, entry
}

func TestBlockRowsEvictionRetainsExactChunks(t *testing.T) {
	// Sizes are deliberately unequal. Expected survivors are hand-selected
	// from the protected ends and the byte shortfall, not another eviction
	// implementation. SQL setup expresses sparse/zero/negative sequence IDs
	// and an absent reservation, which the normal append API cannot request.
	for _, tc := range []struct {
		name    string
		seq     []int
		sizes   []int
		head    *int64
		cap     int64
		keep    []int
		bytes   int64
		lost    uint64
		dropped uint64
	}{
		{"below cap", []int{1, 3, 7}, []int{2, 1, 3}, nil, 8, []int{0, 1, 2}, 6, 0, 0},
		{"exact cap", []int{1, 3, 7}, []int{2, 1, 3}, nil, 6, []int{0, 1, 2}, 6, 0, 0},
		{"stop before newest middle", []int{1, 3, 7, 9, 13, 21}, []int{2, 2, 1, 2, 3, 3}, new(int64(3)), 12, []int{0, 1, 3, 4, 5}, 12, 2, 1},
		{"multiple sparse victims", []int{1, 3, 7, 9, 13, 21}, []int{2, 2, 1, 2, 3, 3}, new(int64(3)), 10, []int{0, 1, 4, 5}, 10, 2, 2},
		{"protected ends exceed cap", []int{1, 3, 7, 9, 13, 21}, []int{2, 2, 1, 2, 3, 4}, new(int64(3)), 10, []int{0, 1, 4, 5}, 11, 2, 2},
		{"overlapping protected ends", []int{1, 3, 7}, []int{3, 3, 3}, new(int64(7)), 8, []int{0, 1, 2}, 9, 2, 0},
		{"no head sparse prefix", []int{1, 3, 7, 9, 13}, []int{1, 3, 2, 2, 4}, nil, 8, []int{2, 3, 4}, 8, 0, 0},
		{"zero victim stops after negative seq", []int{-3, 0, 5, 9}, []int{2, 2, 2, 6}, nil, 8, []int{1, 2, 3}, 10, 0, 0},
		{"zero first victim", []int{0, 5, 9}, []int{2, 2, 6}, nil, 8, []int{0, 1, 2}, 10, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, entry := newBlockRowsEvictionStore(t)
			ctx := context.Background()
			const unit = 512
			payload := blockRowsPayload{NextRow: uint64(len(tc.seq)) + tc.lost, LostRows: tc.lost}
			if tc.head != nil {
				payload.HeadEnd = new(*tc.head * unit)
			}
			bodies := make([][]byte, len(tc.seq))
			for i := range uint64(len(tc.seq)) {
				seq := tc.seq[i]
				from := i
				if i > 0 {
					from += tc.lost
				}
				line, err := encodeBlockRowsLine(from, emulator.Row{Cells: []emulator.Cell{{
					Grapheme: fmt.Sprintf("row-%d-e\u0301", i), Width: emulator.WidthNarrow, HasText: true,
					Style: emulator.Style{Attributes: emulator.AttrBold},
				}}})
				if err != nil {
					t.Fatal(err)
				}
				// JSON permits trailing whitespace; preserve a single newline
				// per row while making each chunk's byte size explicit.
				body := append(line[:len(line)-1], bytes.Repeat([]byte(" "), tc.sizes[i]*unit-len(line))...)
				body = append(body, '\n')
				bodies[i] = body
				payload.Appended += int64(len(body))
				if _, err := s.db.ExecContext(ctx, `INSERT INTO artifact_chunks (artifact_id, seq, body) VALUES (?, ?, ?)`, evictionArtifact, seq, body); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE artifacts SET byte_len = ?, payload = ? WHERE id = ?`, payload.Appended, payload.json(), evictionArtifact); err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if err = evictBlockRowsToCap(ctx, tx, evictionArtifact, tc.cap*unit); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			summary, err := s.CloseBlockRows(ctx, CloseBlockRows{EntryID: entry, ArtifactID: evictionArtifact})
			if err != nil {
				t.Fatal(err)
			}
			if summary.DroppedRows != tc.dropped || summary.LostRows != tc.lost || summary.UnavailableRows != 0 {
				t.Fatalf("summary = %+v, want dropped %d, lost %d", summary, tc.dropped, tc.lost)
			}
			art, err := s.Artifact(ctx, evictionArtifact)
			if err != nil {
				t.Fatal(err)
			}
			want := make([][]byte, len(tc.keep))
			for i, index := range tc.keep {
				want[i] = bodies[index]
			}
			if !reflect.DeepEqual(art.Chunks, want) {
				t.Fatalf("retained chunks differ from indices %v (contents, styles, order or boundaries changed)", tc.keep)
			}
			if art.ByteLen != tc.bytes*unit {
				t.Fatalf("byte_len = %d, want %d", art.ByteLen, tc.bytes*unit)
			}
			if tc.dropped > 0 && (art.Truncated == nil || *art.Truncated != TruncCap) {
				t.Fatalf("truncation = %v, want cap", art.Truncated)
			}
		})
	}
}

func TestBlockRowsAppendRollsBackEvictionFailures(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER eviction_failure BEFORE DELETE ON artifact_chunks
		 BEGIN SELECT RAISE(ABORT, 'delete refused'); END`,
		`CREATE TRIGGER eviction_failure BEFORE UPDATE OF byte_len ON artifacts
		 WHEN NEW.byte_len < OLD.byte_len
		 BEGIN SELECT RAISE(ABORT, 'accounting refused'); END`,
	} {
		t.Run(trigger, func(t *testing.T) {
			s, entry := newBlockRowsEvictionStore(t)
			ctx := context.Background()
			s.policy.SetOutputCapBytes(1024)
			row := emulator.Row{Cells: []emulator.Cell{{Grapheme: "retained", Width: emulator.WidthNarrow, HasText: true}}}
			for i := range uint64(40) {
				if err := s.AppendBlockRows(ctx, AppendBlockRows{EntryID: entry, ArtifactID: evictionArtifact, FromRow: i, Rows: []emulator.Row{row}}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.Artifact(ctx, evictionArtifact)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.ExecContext(ctx, trigger); err != nil {
				t.Fatal(err)
			}
			batch := make([]emulator.Row, 32)
			for i := range batch {
				batch[i] = row
			}
			in := AppendBlockRows{EntryID: entry, ArtifactID: evictionArtifact, FromRow: 40, Rows: batch}
			if err = s.AppendBlockRows(ctx, in); err == nil {
				t.Fatal("append succeeded despite refused eviction")
			}
			after, err := s.Artifact(ctx, evictionArtifact)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed eviction changed retained rows, byte_len or payload cursor")
			}
			if _, err := s.db.ExecContext(ctx, `DROP TRIGGER eviction_failure`); err != nil {
				t.Fatal(err)
			}
			if err := s.AppendBlockRows(ctx, in); err != nil {
				t.Fatalf("same delivery after removing fault: %v", err)
			}
			if _, err := s.CloseBlockRows(ctx, CloseBlockRows{EntryID: entry, ArtifactID: evictionArtifact}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The append owns this transaction; cancellation must leave its prior bytes
// intact, just like a statement failure, rather than commit a partial cap pass.
func TestBlockRowsEvictionCancelledTransaction(t *testing.T) {
	s, _ := newBlockRowsEvictionStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err = evictBlockRowsToCap(ctx, tx, evictionArtifact, 0); err == nil {
		t.Fatal("cancelled eviction succeeded")
	}
	_ = tx.Rollback()
	art, err := s.Artifact(context.Background(), evictionArtifact)
	if err != nil || art == nil || art.ByteLen != 0 || len(art.Chunks) != 0 {
		t.Fatalf("cancelled transaction changed empty artifact: %+v, %v", art, err)
	}
}
