//go:build ignore

// Retained real-driver evidence; not a production implementation or repository test.
// Run from the nocx worktree: go run docs/research/2026-11-07-ledger-read-proof/adiantum_probe.go
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func require(ok bool, what string) {
	if !ok {
		panic(what)
	}
}

func keyedURI(path, key string) string {
	// The same destination-keying pattern as internal/content/sqlite.go:keyedURI.
	return "file:" + path + "?vfs=adiantum&hexkey=" + key
}

func open(path, key, journal string) *sql.DB {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	must(err)
	must(file.Close())
	db, err := driver.Open(keyedURI(path, key), func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA temp_store=MEMORY; PRAGMA cache_size=-64; PRAGMA journal_mode=" + journal)
	})
	must(err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db
}

func canaryCheck(dir string, canary []byte) {
	entries, err := os.ReadDir(dir)
	must(err)
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		must(os.Chmod(path, 0o600))
		info, err := os.Stat(path)
		must(err)
		require(info.Mode().Perm() == 0o600, "database sidecar mode is not0600")
		data, err := os.ReadFile(path)
		must(err)
		require(!bytes.Contains(data, canary), "plaintext canary in "+entry.Name())
		require(!bytes.HasPrefix(data, []byte("SQLite format 3")), "plaintext SQLite header")
	}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "nocx-encrypted-ledger-spool-")
	must(err)
	defer os.RemoveAll(dir)
	must(os.Chmod(dir, 0o700))
	keyBytes := bytes.Repeat([]byte{0x37}, 32)
	key := hex.EncodeToString(keyBytes)
	sourcePath := filepath.Join(dir, "source.db")
	spoolPath := filepath.Join(dir, "response.db")
	source := open(sourcePath, key, "WAL")
	defer source.Close()
	spool := open(spoolPath, key, "DELETE")
	defer spool.Close()
	_, err = source.ExecContext(ctx, `
		CREATE TABLE metadata(revision INTEGER NOT NULL,state TEXT NOT NULL);
		INSERT INTO metadata VALUES(7,'open');
		CREATE TABLE artifact_chunks(artifact_id TEXT NOT NULL,seq INTEGER NOT NULL,
		 body BLOB NOT NULL,PRIMARY KEY(artifact_id,seq)) STRICT;`)
	must(err)
	_, err = spool.ExecContext(ctx, `
		CREATE TABLE metadata(revision INTEGER NOT NULL,state TEXT NOT NULL);
		CREATE TABLE parts(n INTEGER PRIMARY KEY,body BLOB NOT NULL) STRICT;`)
	must(err)
	// Generation/expected digest belong to the fixture, not capture's memory claim.
	canary := []byte("NOCX-SPOOL-PRIVATE-CANARY-b47d4bdce8f7")
	body := bytes.Repeat([]byte("z"), 9<<20)
	copy(body, canary)
	want := sha256.Sum256(body)
	_, err = source.ExecContext(ctx, "INSERT INTO artifact_chunks VALUES('a',-17,?)", body)
	must(err)
	body = nil

	conn, err := source.Conn(ctx)
	must(err)
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	must(err)
	var rev, rowid int64
	var state string
	must(tx.QueryRowContext(ctx, "SELECT revision,state FROM metadata").Scan(&rev, &state))
	must(tx.QueryRowContext(ctx, "SELECT rowid FROM artifact_chunks WHERE artifact_id='a' AND seq=-17").Scan(&rowid))
	dst, err := spool.BeginTx(ctx, nil)
	must(err)
	_, err = dst.ExecContext(ctx, "INSERT INTO metadata VALUES(?,?)", rev, state)
	must(err)
	started := time.Now()
	parts, copied, maxSlice := 0, 0, 0
	must(conn.Raw(func(raw any) error {
		c, ok := raw.(driver.Conn)
		if !ok {
			return fmt.Errorf("unexpected driver %T", raw)
		}
		blob, err := c.Raw().OpenBlob("main", "artifact_chunks", "body", rowid, false)
		if err != nil {
			return err
		}
		defer blob.Close()
		buf := make([]byte, 16<<10)
		for {
			n, readErr := blob.Read(buf)
			if n > 0 {
				if _, err := dst.ExecContext(ctx, "INSERT INTO parts VALUES(?,?)", parts, buf[:n]); err != nil {
					return err
				}
				parts++
				copied += n
				if n > maxSlice {
					maxSlice = n
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		return nil
	}))
	must(dst.Commit())
	must(tx.Commit())
	must(conn.Close())
	capture := time.Since(started)
	require(copied == 9<<20 && maxSlice <= 16<<10, "bounded incremental BLOB copy mismatch")
	// Source writer runs after capture and BEFORE any spool page is read.
	// No source transaction/connection is pinned for network continuation.
	_, err = source.ExecContext(ctx, "UPDATE metadata SET revision=8,state='sealed'; DELETE FROM artifact_chunks")
	must(err)
	var current int
	must(source.QueryRowContext(ctx, "SELECT revision FROM metadata").Scan(&current))
	require(current == 8, "writer could not advance independently of pagination")
	must(spool.QueryRowContext(ctx, "SELECT revision,state FROM metadata").Scan(&rev, &state))
	require(rev == 7 && state == "open", "spool mixed source metadata revisions")
	hash := sha256.New()
	rows, err := spool.QueryContext(ctx, "SELECT body FROM parts ORDER BY n")
	must(err)
	readParts := 0
	for rows.Next() {
		var piece []byte
		must(rows.Scan(&piece))
		require(len(piece) <= 16<<10, "spool page grew beyond slice")
		_, err := hash.Write(piece)
		must(err)
		readParts++
	}
	must(rows.Err())
	must(rows.Close())
	require(bytes.Equal(hash.Sum(nil), want[:]) && readParts == parts, "snapshot bytes changed under source deletion")
	canaryCheck(dir, canary)
	must(spool.Close())
	// Reopen only after close: never two VFS handles to the SAME file.
	wrong, err := driver.Open(keyedURI(spoolPath, hex.EncodeToString(bytes.Repeat([]byte{0x38}, 32))))
	var count int
	if err == nil {
		err = wrong.QueryRowContext(ctx, "SELECT count(*) FROM parts").Scan(&count)
		require(err != nil, "wrong key unexpectedly read encrypted spool")
		must(wrong.Close())
	}
	right, err := driver.Open(keyedURI(spoolPath, key))
	must(err)
	must(right.QueryRowContext(ctx, "SELECT count(*) FROM parts").Scan(&count))
	require(count == parts, "correct-key reopen lost snapshot parts")
	must(right.Close())
	info, err := os.Stat(spoolPath)
	must(err)
	must(os.Remove(spoolPath))
	_, err = os.Stat(spoolPath)
	require(os.IsNotExist(err), "completed response spool was not removed")
	result := map[string]any{
		"kind": "real-adiantum-incremental-blob-probe", "copiedBytes": copied,
		"parts": parts, "maxCopySliceBytes": maxSlice, "snapshotRevision": rev,
		"sourceRevisionAfterDelete": current, "spoolFileBytes": info.Size(),
		"captureMilliseconds": capture.Milliseconds(), "canaryAbsent": true,
		"wrongKeyRejected": true, "correctKeyReopen": true, "cleanup": true,
		"productionReadAPIImplemented": false,
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	must(err)
	fmt.Println(string(encoded))
	fmt.Println("PASS: real encrypted spool, bounded incremental copy, source progress and cleanup")
}
