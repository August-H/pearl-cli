package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=5000&_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, databaseFile := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(databaseFile, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = db.Close()
			return nil, fmt.Errorf("secure database file: %w", err)
		}
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    prompt TEXT NOT NULL,
    workspace_root TEXT NOT NULL,
    status TEXT NOT NULL,
    result TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    transcript BLOB,
    input_question TEXT NOT NULL DEFAULT '',
    input_tool_call_id TEXT NOT NULL DEFAULT '',
    input_response TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    archived_at TEXT
);
CREATE INDEX IF NOT EXISTS jobs_queue ON jobs(status, created_at);

CREATE TABLE IF NOT EXISTS events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    data TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_job_sequence ON events(job_id, sequence);

CREATE TABLE IF NOT EXISTS tool_executions (
    job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    arguments TEXT NOT NULL,
    result TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(job_id, tool_call_id)
);

CREATE TABLE IF NOT EXISTS schedules (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    prompt TEXT NOT NULL,
    workspace_root TEXT NOT NULL,
    interval_seconds INTEGER NOT NULL,
    next_run_at TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS schedules_due ON schedules(enabled, next_run_at);

CREATE TABLE IF NOT EXISTS autonomous_sessions (
    id TEXT PRIMARY KEY,
    goal TEXT NOT NULL,
    workspace_root TEXT NOT NULL,
    status TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    finished_at TEXT
);
CREATE INDEX IF NOT EXISTS autonomous_sessions_status
ON autonomous_sessions(status, updated_at);

CREATE TABLE IF NOT EXISTS autonomous_jobs (
    session_id TEXT NOT NULL REFERENCES autonomous_sessions(id) ON DELETE CASCADE,
    job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY(session_id, job_id)
);
CREATE INDEX IF NOT EXISTS autonomous_jobs_session
ON autonomous_jobs(session_id, created_at);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "name", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "input_question", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "input_tool_call_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "input_response", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "archived_at", definition: "TEXT"},
	} {
		if err := s.ensureColumn(ctx, "jobs", column.name, column.definition); err != nil {
			return err
		}
	}
	if err := s.migrateStoredJobNames(ctx); err != nil {
		return err
	}
	if err := s.migrateLegacyJobIDs(ctx); err != nil {
		return err
	}
	return s.migrateErroredPendingJobs(ctx)
}

func (s *Store) migrateErroredPendingJobs(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs
SET status = ?
WHERE status = ?
AND error <> ''
AND finished_at IS NOT NULL
AND archived_at IS NULL`, JobFailed, JobPending)
	return err
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var id, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&id, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(
		"ALTER TABLE %s ADD COLUMN %s %s", table, column, definition,
	))
	return err
}

func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func parseOptionalTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func newID(prefix string) string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func scanJob(scanner interface{ Scan(...any) error }) (Job, error) {
	var job Job
	var created string
	var started, finished, archived sql.NullString
	var cancelRequested int
	err := scanner.Scan(
		&job.ID, &job.Prompt, &job.WorkspaceRoot, &job.Status, &job.Result,
		&job.Error, &cancelRequested, &job.Question, &job.InputToolCallID,
		&job.InputResponse, &created, &started, &finished, &archived,
	)
	if err != nil {
		return Job{}, err
	}
	job.CancelRequested = cancelRequested != 0
	job.CreatedAt, err = parseTime(created)
	if err != nil {
		return Job{}, err
	}
	job.StartedAt, err = parseOptionalTime(started)
	if err != nil {
		return Job{}, err
	}
	job.FinishedAt, err = parseOptionalTime(finished)
	if err != nil {
		return Job{}, err
	}
	job.ArchivedAt, err = parseOptionalTime(archived)
	return job, err
}

const jobColumns = `id, prompt, workspace_root, status, result, error,
cancel_requested, input_question, input_tool_call_id, input_response,
created_at, started_at, finished_at, archived_at`

func isNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
