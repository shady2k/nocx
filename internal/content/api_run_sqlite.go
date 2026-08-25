package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	apiRunSchemaVersion = 1
	apiRunChunkBytes    = 64 * 1024

	apiRunArtifactRequest      = "request"
	apiRunArtifactResponseText = "response-text"
	apiRunArtifactResponseRaw  = "response-raw"
)

// migrateAPIRuns is a feature-owned migration inside ContentDB's open path.
// The ledger's schema version deliberately does not advance for an API feature:
// each typed repository owns a monotonic version, so adding runs cannot force
// an unrelated ledger reset. The tables are still listed in rebuildDropOrder,
// so a base-schema rebuild remains all-or-nothing and foreign-key complete.
func migrateAPIRuns(ctx context.Context, conn *sql.Conn) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("content: api runs: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	statements := []string{
		`CREATE TABLE IF NOT EXISTS api_run_schema (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			version INTEGER NOT NULL
		) STRICT`,
		`CREATE TABLE IF NOT EXISTS api_runs (
			id              INTEGER PRIMARY KEY,
			collection_path  TEXT NOT NULL,
			request_rel_path TEXT NOT NULL,
			repeated_from    INTEGER REFERENCES api_runs(id) ON DELETE SET NULL,
			method           TEXT NOT NULL,
			url              TEXT NOT NULL,
			outcome          TEXT NOT NULL CHECK (outcome IN ('pending','answered','failed','stopped')),
			request_spans    TEXT NOT NULL DEFAULT '[]',
			metadata         TEXT NOT NULL DEFAULT '{}',
			started_at       INTEGER NOT NULL,
			ended_at         INTEGER,
			logical_bytes    INTEGER NOT NULL DEFAULT 0 CHECK (logical_bytes >= 0)
		) STRICT`,
		`CREATE TABLE IF NOT EXISTS api_run_artifacts (
			id          INTEGER PRIMARY KEY,
			run_id      INTEGER NOT NULL REFERENCES api_runs(id) ON DELETE CASCADE,
			kind        TEXT NOT NULL CHECK (kind IN ('request','response-text','response-raw')),
			byte_len    INTEGER NOT NULL DEFAULT 0 CHECK (byte_len >= 0),
			chunk_count INTEGER NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
			UNIQUE (run_id, kind)
		) STRICT`,
		`CREATE TABLE IF NOT EXISTS api_run_artifact_chunks (
			artifact_id INTEGER NOT NULL REFERENCES api_run_artifacts(id) ON DELETE CASCADE,
			seq         INTEGER NOT NULL CHECK (seq >= 1),
			body        BLOB NOT NULL,
			PRIMARY KEY (artifact_id, seq)
		) STRICT`,
		`CREATE INDEX IF NOT EXISTS api_runs_by_request
			ON api_runs(collection_path, request_rel_path, started_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS api_run_artifacts_by_run
			ON api_run_artifacts(run_id, kind)`,
	}
	for i, statement := range statements {
		if _, execErr := tx.ExecContext(ctx, statement); execErr != nil {
			return fmt.Errorf("content: api runs: migration statement %d: %w", i+1, execErr)
		}
	}
	var version int
	err = tx.QueryRowContext(ctx, `SELECT version FROM api_run_schema WHERE id = 1`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, execErr := tx.ExecContext(ctx, `INSERT INTO api_run_schema(id, version) VALUES (1, ?)`, apiRunSchemaVersion); execErr != nil {
			return fmt.Errorf("content: api runs: seed migration version: %w", execErr)
		}
	case err != nil:
		return fmt.Errorf("content: api runs: read migration version: %w", err)
	case version > apiRunSchemaVersion:
		return fmt.Errorf("content: api runs: version %d is newer than supported version %d", version, apiRunSchemaVersion)
	case version < apiRunSchemaVersion:
		// Version 1 is the first release. Future versions add their own
		// transactional step here instead of resetting the ledger.
		if _, err := tx.ExecContext(ctx, `UPDATE api_run_schema SET version = ? WHERE id = 1`, apiRunSchemaVersion); err != nil {
			return fmt.Errorf("content: api runs: advance migration version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("content: api runs: commit migration: %w", err)
	}
	return nil
}

var _ APIRunRepository = (*sqliteContent)(nil)

// APIRuns returns the feature-owned API exchange repository. It shares the
// ContentDB writer but never writes the ledger's artifacts table.
func (s *sqliteContent) APIRuns() APIRunRepository { return s }

func validateAPIRunStart(start APIRunStart) error {
	if start.CollectionPath == "" {
		return errors.New("content: api run: collection path is empty")
	}
	if start.RequestRelPath == "" {
		return errors.New("content: api run: request relative path is empty")
	}
	if start.StartedAt < 0 {
		return errors.New("content: api run: started at is negative")
	}
	return nil
}

func validateAPIRunResult(result APIRunResult) error {
	switch result.Outcome {
	case APIRunAnswered:
		if result.Response == nil || result.Failure != nil {
			return errors.New("content: api run: answered outcome requires a response and no failure")
		}
	case APIRunFailed, APIRunStopped:
		if result.Response != nil || result.Failure == nil {
			return errors.New("content: api run: failed or stopped outcome requires a failure and no response")
		}
	default:
		return fmt.Errorf("content: api run: invalid completed outcome %q", result.Outcome)
	}
	if result.EndedAt < 0 {
		return errors.New("content: api run: ended at is negative")
	}
	return nil
}

type apiRunMetadata struct {
	Environment  string              `json:"environment"`
	Route        APIRunRoute         `json:"route"`
	RemoteAddr   string              `json:"remoteAddr"`
	DNSAddresses []string            `json:"dnsAddresses"`
	Timings      APIRunTimings       `json:"timings"`
	Certificates []APIRunCertificate `json:"certificates"`
	Failure      *APIRunFailure      `json:"failure"`
	Response     *apiRunResponseMeta `json:"response"`
}

type apiRunResponseMeta struct {
	Status         int            `json:"status"`
	Headers        []APIRunHeader `json:"headers"`
	Binary         bool           `json:"binary"`
	Lossy          bool           `json:"lossy"`
	Truncated      bool           `json:"truncated"`
	Size           int64          `json:"size"`
	TLSVersion     string         `json:"tlsVersion"`
	RawSpans       []APIRunSpan   `json:"rawSpans"`
	TLSCipherSuite string         `json:"tlsCipherSuite"`
	Trust          APIRunTrust    `json:"trust"`
}

func metadataFor(result APIRunResult) (string, error) {
	meta := apiRunMetadata{
		Environment:  result.Environment,
		Route:        result.Route,
		RemoteAddr:   result.RemoteAddr,
		DNSAddresses: nonNilStrings(result.DNSAddresses),
		Timings:      result.Timings,
		Certificates: nonNilCertificates(result.Certificates),
		Failure:      result.Failure,
	}
	if result.Response != nil {
		meta.Response = &apiRunResponseMeta{
			Status:         result.Response.Status,
			Headers:        nonNilHeaders(result.Response.Headers),
			Binary:         result.Response.Binary,
			Lossy:          result.Response.Lossy,
			Truncated:      result.Response.Truncated,
			Size:           result.Response.Size,
			TLSVersion:     result.Response.TLSVersion,
			RawSpans:       nonNilSpans(result.Response.Raw.Spans),
			TLSCipherSuite: result.Response.TLSCipherSuite,
			Trust:          result.Response.Trust,
		}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("content: api run: encode metadata: %w", err)
	}
	return string(raw), nil
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonNilSpans(in []APIRunSpan) []APIRunSpan {
	if in == nil {
		return []APIRunSpan{}
	}
	return in
}

func nonNilHeaders(in []APIRunHeader) []APIRunHeader {
	if in == nil {
		return []APIRunHeader{}
	}
	return in
}

func nonNilCertificates(in []APIRunCertificate) []APIRunCertificate {
	if in == nil {
		return []APIRunCertificate{}
	}
	return in
}

func (s *sqliteContent) Begin(ctx context.Context, start APIRunStart) (APIRun, error) {
	if err := validateAPIRunStart(start); err != nil {
		return APIRun{}, err
	}
	spans, err := json.Marshal(nonNilSpans(start.Request.Spans))
	if err != nil {
		return APIRun{}, fmt.Errorf("content: api run: encode request spans: %w", err)
	}
	var id int64
	err = s.run(ctx, func(ctx context.Context) error {
		tx, txErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if txErr != nil {
			return fmt.Errorf("content: api run: begin: %w", txErr)
		}
		defer func() { _ = tx.Rollback() }()

		if insertErr := tx.QueryRowContext(ctx, `INSERT INTO api_runs
			(collection_path, request_rel_path, repeated_from, method, url, outcome,
			 request_spans, metadata, started_at)
			VALUES (?, ?, ?, ?, ?, 'pending', ?, '{}', ?)
			RETURNING id`, start.CollectionPath, start.RequestRelPath, start.RepeatedFrom,
			start.Method, start.URL, string(spans), start.StartedAt).Scan(&id); insertErr != nil {
			return fmt.Errorf("content: api run: insert: %w", insertErr)
		}
		if artifactErr := insertAPIRunArtifact(ctx, tx, id, apiRunArtifactRequest, start.Request.Text); artifactErr != nil {
			return artifactErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return fmt.Errorf("content: api run: commit begin: %w", commitErr)
		}
		return nil
	})
	if err != nil {
		return APIRun{}, err
	}
	return APIRun{
		ID:             id,
		CollectionPath: start.CollectionPath,
		RequestRelPath: start.RequestRelPath,
		RepeatedFrom:   start.RepeatedFrom,
		Method:         start.Method,
		URL:            start.URL,
		Outcome:        APIRunPending,
		Request:        APIRaw{Text: start.Request.Text, Spans: nonNilSpans(start.Request.Spans)},
		StartedAt:      start.StartedAt,
	}, nil
}

func insertAPIRunArtifact(ctx context.Context, tx *sql.Tx, runID int64, kind, body string) error {
	var artifactID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO api_run_artifacts(run_id, kind)
		VALUES (?, ?) RETURNING id`, runID, kind).Scan(&artifactID); err != nil {
		return fmt.Errorf("content: api run: insert %s artifact: %w", kind, err)
	}
	chunks := splitAPIRunChunks([]byte(body))
	for i, chunk := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO api_run_artifact_chunks(artifact_id, seq, body)
			VALUES (?, ?, ?)`, artifactID, i+1, chunk); err != nil {
			return fmt.Errorf("content: api run: insert %s chunk %d: %w", kind, i+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE api_run_artifacts
		SET byte_len = ?, chunk_count = ? WHERE id = ?`, len(body), len(chunks), artifactID); err != nil {
		return fmt.Errorf("content: api run: finish %s artifact: %w", kind, err)
	}
	return nil
}

func splitAPIRunChunks(body []byte) [][]byte {
	if len(body) == 0 {
		return nil
	}
	chunks := make([][]byte, 0, (len(body)+apiRunChunkBytes-1)/apiRunChunkBytes)
	for len(body) > 0 {
		n := apiRunChunkBytes
		if n > len(body) {
			n = len(body)
		}
		chunks = append(chunks, body[:n])
		body = body[n:]
	}
	return chunks
}

func (s *sqliteContent) Complete(ctx context.Context, id int64, result APIRunResult) (APIRun, error) {
	if id <= 0 {
		return APIRun{}, errors.New("content: api run: id must be positive")
	}
	if err := validateAPIRunResult(result); err != nil {
		return APIRun{}, err
	}
	metadata, err := metadataFor(result)
	if err != nil {
		return APIRun{}, err
	}
	var start APIRunStart
	err = s.run(ctx, func(ctx context.Context) error {
		tx, txErr := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if txErr != nil {
			return fmt.Errorf("content: api run: begin complete: %w", txErr)
		}
		defer func() { _ = tx.Rollback() }()

		var spansJSON string
		if scanErr := tx.QueryRowContext(ctx, `SELECT collection_path, request_rel_path,
			repeated_from, method, url, request_spans, started_at
			FROM api_runs WHERE id = ? AND outcome = 'pending'`, id).
			Scan(&start.CollectionPath, &start.RequestRelPath, &start.RepeatedFrom,
				&start.Method, &start.URL, &spansJSON, &start.StartedAt); scanErr != nil {
			if errors.Is(scanErr, sql.ErrNoRows) {
				return fmt.Errorf("content: api run %d: %w", id, ErrNotFound)
			}
			return fmt.Errorf("content: api run: read pending row: %w", scanErr)
		}
		if decodeErr := json.Unmarshal([]byte(spansJSON), &start.Request.Spans); decodeErr != nil {
			return fmt.Errorf("content: api run: decode request spans: %w", decodeErr)
		}
		requestText, readErr := readAPIRunArtifactTx(ctx, tx, id, apiRunArtifactRequest)
		if readErr != nil {
			return readErr
		}
		start.Request.Text = requestText

		if result.Response != nil {
			if artifactErr := insertAPIRunArtifact(ctx, tx, id, apiRunArtifactResponseText, result.Response.Text); artifactErr != nil {
				return artifactErr
			}
			if artifactErr := insertAPIRunArtifact(ctx, tx, id, apiRunArtifactResponseRaw, result.Response.Raw.Text); artifactErr != nil {
				return artifactErr
			}
		}
		if _, updateErr := tx.ExecContext(ctx, `UPDATE api_runs SET outcome = ?, metadata = ?, ended_at = ?,
			logical_bytes = (SELECT COALESCE(SUM(byte_len), 0) FROM api_run_artifacts WHERE run_id = ?)
			WHERE id = ? AND outcome = 'pending'`, result.Outcome, metadata, result.EndedAt, id, id); updateErr != nil {
			return fmt.Errorf("content: api run: complete: %w", updateErr)
		}
		if evictErr := evictAPIRunsTx(ctx, tx, s.cfg.Budget.RetentionBytes); evictErr != nil {
			return evictErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return fmt.Errorf("content: api run: commit complete: %w", commitErr)
		}
		return nil
	})
	if err != nil {
		return APIRun{}, err
	}
	return apiRunFromStartResult(id, start, result), nil
}

func apiRunFromStartResult(id int64, start APIRunStart, result APIRunResult) APIRun {
	ended := result.EndedAt
	return APIRun{
		ID:             id,
		CollectionPath: start.CollectionPath,
		RequestRelPath: start.RequestRelPath,
		RepeatedFrom:   start.RepeatedFrom,
		Method:         start.Method,
		URL:            start.URL,
		Outcome:        result.Outcome,
		Environment:    result.Environment,
		Route:          result.Route,
		Request:        start.Request,
		RemoteAddr:     result.RemoteAddr,
		DNSAddresses:   nonNilStrings(result.DNSAddresses),
		Timings:        result.Timings,
		Certificates:   nonNilCertificates(result.Certificates),
		Response:       result.Response,
		Failure:        result.Failure,
		StartedAt:      start.StartedAt,
		EndedAt:        &ended,
	}
}

func evictAPIRunsTx(ctx context.Context, tx *sql.Tx, budget int64) error {
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(logical_bytes), 0)
		FROM api_runs WHERE outcome != 'pending' AND ended_at IS NOT NULL`).Scan(&total); err != nil {
		return fmt.Errorf("content: api run: retention total: %w", err)
	}
	for total > budget {
		var id, bytes int64
		if err := tx.QueryRowContext(ctx, `SELECT id, logical_bytes FROM api_runs
			WHERE outcome != 'pending' AND ended_at IS NOT NULL
			ORDER BY ended_at, id LIMIT 1`).Scan(&id, &bytes); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("content: api run: retention victim: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM api_runs WHERE id = ?`, id); err != nil {
			return fmt.Errorf("content: api run: retention delete %d: %w", id, err)
		}
		total -= bytes
	}
	return nil
}

type storedAPIRun struct {
	id             int64
	collectionPath string
	requestPath    string
	repeatedFrom   *int64
	method         string
	url            string
	outcome        APIRunOutcome
	requestSpans   string
	metadata       string
	startedAt      int64
	endedAt        *int64
}

func (s *sqliteContent) Get(ctx context.Context, id int64) (APIRun, error) {
	if id <= 0 {
		return APIRun{}, errors.New("content: api run: id must be positive")
	}
	var row storedAPIRun
	if err := s.db.QueryRowContext(ctx, `SELECT id, collection_path, request_rel_path,
		repeated_from, method, url, outcome, request_spans, metadata, started_at, ended_at
		FROM api_runs WHERE id = ?`, id).Scan(
		&row.id, &row.collectionPath, &row.requestPath, &row.repeatedFrom, &row.method,
		&row.url, &row.outcome, &row.requestSpans, &row.metadata, &row.startedAt, &row.endedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIRun{}, fmt.Errorf("content: api run %d: %w", id, ErrNotFound)
		}
		return APIRun{}, fmt.Errorf("content: api run: get: %w", err)
	}
	return s.hydrateAPIRun(ctx, row)
}

func (s *sqliteContent) List(ctx context.Context, collectionPath, requestRelPath string) ([]APIRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, collection_path, request_rel_path,
		repeated_from, method, url, outcome, request_spans, metadata, started_at, ended_at
		FROM api_runs WHERE collection_path = ? AND request_rel_path = ?
		ORDER BY started_at DESC, id DESC`, collectionPath, requestRelPath)
	if err != nil {
		return nil, fmt.Errorf("content: api run: list: %w", err)
	}
	var stored []storedAPIRun
	for rows.Next() {
		var row storedAPIRun
		if err := rows.Scan(&row.id, &row.collectionPath, &row.requestPath, &row.repeatedFrom,
			&row.method, &row.url, &row.outcome, &row.requestSpans, &row.metadata, &row.startedAt, &row.endedAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("content: api run: list row: %w", err)
		}
		stored = append(stored, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("content: api run: list rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("content: api run: close list: %w", err)
	}
	out := make([]APIRun, 0, len(stored))
	for _, row := range stored {
		run, err := s.hydrateAPIRun(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

func (s *sqliteContent) hydrateAPIRun(ctx context.Context, row storedAPIRun) (APIRun, error) {
	var meta apiRunMetadata
	if err := json.Unmarshal([]byte(row.metadata), &meta); err != nil {
		return APIRun{}, fmt.Errorf("content: api run: decode metadata: %w", err)
	}
	var spans []APIRunSpan
	if err := json.Unmarshal([]byte(row.requestSpans), &spans); err != nil {
		return APIRun{}, fmt.Errorf("content: api run: decode request spans: %w", err)
	}
	requestText, err := s.readAPIRunArtifact(ctx, row.id, apiRunArtifactRequest)
	if err != nil {
		return APIRun{}, err
	}
	run := APIRun{
		ID:             row.id,
		CollectionPath: row.collectionPath,
		RequestRelPath: row.requestPath,
		RepeatedFrom:   row.repeatedFrom,
		Method:         row.method,
		URL:            row.url,
		Outcome:        row.outcome,
		Environment:    meta.Environment,
		Route:          meta.Route,
		Request:        APIRaw{Text: requestText, Spans: nonNilSpans(spans)},
		RemoteAddr:     meta.RemoteAddr,
		DNSAddresses:   nonNilStrings(meta.DNSAddresses),
		Timings:        meta.Timings,
		Certificates:   nonNilCertificates(meta.Certificates),
		Failure:        meta.Failure,
		StartedAt:      row.startedAt,
		EndedAt:        row.endedAt,
	}
	if meta.Response != nil {
		responseText, err := s.readAPIRunArtifact(ctx, row.id, apiRunArtifactResponseText)
		if err != nil {
			return APIRun{}, err
		}
		responseRaw, err := s.readAPIRunArtifact(ctx, row.id, apiRunArtifactResponseRaw)
		if err != nil {
			return APIRun{}, err
		}
		run.Response = &APIRunResponse{
			Status:         meta.Response.Status,
			Headers:        nonNilHeaders(meta.Response.Headers),
			Text:           responseText,
			Binary:         meta.Response.Binary,
			Lossy:          meta.Response.Lossy,
			Truncated:      meta.Response.Truncated,
			Size:           meta.Response.Size,
			TLSVersion:     meta.Response.TLSVersion,
			Raw:            APIRaw{Text: responseRaw, Spans: nonNilSpans(meta.Response.RawSpans)},
			TLSCipherSuite: meta.Response.TLSCipherSuite,
			Trust:          meta.Response.Trust,
		}
	}
	return run, nil
}

func readAPIRunArtifactTx(ctx context.Context, tx *sql.Tx, runID int64, kind string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.body FROM api_run_artifact_chunks c
		JOIN api_run_artifacts a ON a.id = c.artifact_id
		WHERE a.run_id = ? AND a.kind = ? ORDER BY c.seq`, runID, kind)
	if err != nil {
		return "", fmt.Errorf("content: api run: read %s artifact: %w", kind, err)
	}
	var b strings.Builder
	for rows.Next() {
		var chunk []byte
		if err := rows.Scan(&chunk); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("content: api run: read %s chunk: %w", kind, err)
		}
		b.Write(chunk)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", fmt.Errorf("content: api run: read %s chunks: %w", kind, err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("content: api run: close %s artifact: %w", kind, err)
	}
	return b.String(), nil
}

func (s *sqliteContent) readAPIRunArtifact(ctx context.Context, runID int64, kind string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.body FROM api_run_artifact_chunks c
		JOIN api_run_artifacts a ON a.id = c.artifact_id
		WHERE a.run_id = ? AND a.kind = ? ORDER BY c.seq`, runID, kind)
	if err != nil {
		return "", fmt.Errorf("content: api run: read %s artifact: %w", kind, err)
	}
	var b strings.Builder
	for rows.Next() {
		var chunk []byte
		if err := rows.Scan(&chunk); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("content: api run: read %s chunk: %w", kind, err)
		}
		b.Write(chunk)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", fmt.Errorf("content: api run: read %s chunks: %w", kind, err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("content: api run: close %s artifact: %w", kind, err)
	}
	return b.String(), nil
}

func (s *sqliteContent) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("content: api run: id must be positive")
	}
	return s.run(ctx, func(ctx context.Context) error {
		res, err := s.db.ExecContext(ctx, `DELETE FROM api_runs WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("content: api run: delete: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("content: api run: delete result: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("content: api run %d: %w", id, ErrNotFound)
		}
		return nil
	})
}
