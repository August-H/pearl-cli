package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) CreateJob(ctx context.Context, prompt, workspaceRoot string) (Job, error) {
	return s.CreateNamedJob(ctx, "", prompt, workspaceRoot)
}

func (s *Store) CreateNamedJob(
	ctx context.Context,
	name, prompt, workspaceRoot string,
) (Job, error) {
	return s.createNamedJob(ctx, name, prompt, workspaceRoot, JobQueued)
}

func (s *Store) CreatePendingNamedJob(
	ctx context.Context,
	name, prompt, workspaceRoot string,
) (Job, error) {
	return s.createNamedJob(ctx, name, prompt, workspaceRoot, JobPending)
}

func (s *Store) createNamedJob(
	ctx context.Context,
	name, prompt, workspaceRoot, status string,
) (Job, error) {
	name = strings.TrimSpace(name)
	if err := ValidateJobName(name); err != nil {
		return Job{}, err
	}
	job := Job{
		Prompt:        prompt,
		WorkspaceRoot: workspaceRoot,
		Status:        status,
		CreatedAt:     time.Now().UTC(),
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer transaction.Rollback()
	if name == "" {
		job.ID, err = availableJobID(ctx, transaction)
		if err != nil {
			return Job{}, err
		}
	} else {
		job.ID = name
		var existing string
		err = transaction.QueryRowContext(
			ctx, "SELECT id FROM jobs WHERE id = ?", job.ID,
		).Scan(&existing)
		if err == nil {
			return Job{}, fmt.Errorf("job ID %q already exists", job.ID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Job{}, fmt.Errorf("check job ID: %w", err)
		}
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO jobs(id, prompt, workspace_root, status, created_at)
VALUES (?, ?, ?, ?, ?)`, job.ID, job.Prompt, job.WorkspaceRoot, job.Status,
		job.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return Job{}, err
	}
	if _, err := appendEventTx(ctx, transaction, job.ID, "status", job.Status); err != nil {
		return Job{}, err
	}
	return job, transaction.Commit()
}

func (s *Store) RunJob(ctx context.Context, id string) (Job, int64, error) {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return Job{}, 0, err
	}
	if err := rejectArchivedJob(job); err != nil {
		return Job{}, 0, err
	}
	switch job.Status {
	case JobPending:
		return s.QueueJob(ctx, id)
	case JobCompleted, JobFailed, JobInterrupted, JobCancelled:
		return s.RetryJob(ctx, id)
	default:
		return Job{}, 0, fmt.Errorf("job %q cannot be run from status %q", id, job.Status)
	}
}

func (s *Store) ArchiveJob(ctx context.Context, id string) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	var status string
	var archived sql.NullString
	err = transaction.QueryRowContext(
		ctx, "SELECT status, archived_at FROM jobs WHERE id = ?", id,
	).Scan(&status, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("job %q not found", id)
	}
	if err != nil {
		return err
	}
	if archived.Valid {
		return fmt.Errorf("job %q is already archived", id)
	}
	if status == JobQueued || status == JobRunning {
		return fmt.Errorf("job %q cannot be archived while status is %q", id, status)
	}
	archivedAt := nowText()
	result, err := transaction.ExecContext(ctx,
		"UPDATE jobs SET archived_at = ? WHERE id = ? AND archived_at IS NULL",
		archivedAt, id,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("job %q was not archived", id)
	}
	if _, err := appendEventTx(ctx, transaction, id, "archived", archivedAt); err != nil {
		return err
	}
	return transaction.Commit()
}

func (s *Store) QueueJob(ctx context.Context, id string) (Job, int64, error) {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return Job{}, 0, err
	}
	if err := rejectArchivedJob(job); err != nil {
		return Job{}, 0, err
	}
	if job.Status != JobPending {
		return Job{}, 0, fmt.Errorf("job %q cannot be run from status %q", id, job.Status)
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, 0, err
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `
UPDATE jobs SET status = ?, error = '', result = '', cancel_requested = 0,
started_at = NULL, finished_at = NULL WHERE id = ? AND status = ? AND archived_at IS NULL`,
		JobQueued, id, JobPending)
	if err != nil {
		return Job{}, 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Job{}, 0, err
	}
	if affected != 1 {
		return Job{}, 0, fmt.Errorf("job %q is no longer pending", id)
	}
	eventSequence, err := appendEventTx(ctx, transaction, id, "status", JobQueued)
	if err != nil {
		return Job{}, 0, err
	}
	if err := transaction.Commit(); err != nil {
		return Job{}, 0, err
	}
	job.Status = JobQueued
	job.Error = ""
	job.Result = ""
	job.CancelRequested = false
	job.StartedAt = nil
	job.FinishedAt = nil
	return job, eventSequence, nil
}

func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	job, err := scanJob(s.db.QueryRowContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE id = ?", id))
	if isNotFound(err) {
		return Job{}, fmt.Errorf("job %q not found", id)
	}
	return job, err
}

func (s *Store) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE archived_at IS NULL ORDER BY created_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

func (s *Store) ListArchivedJobs(ctx context.Context, limit int) ([]Job, error) {
	query := "SELECT " + jobColumns +
		" FROM jobs WHERE archived_at IS NOT NULL ORDER BY archived_at DESC"
	var (
		rows *sql.Rows
		err  error
	)
	if limit > 0 {
		if limit > 500 {
			limit = 500
		}
		rows, err = s.db.QueryContext(ctx, query+" LIMIT ?", limit)
	} else {
		rows, err = s.db.QueryContext(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

func scanJobs(rows *sql.Rows) ([]Job, error) {
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) ListActiveJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+jobColumns+` FROM jobs
WHERE archived_at IS NULL AND status IN (?, ?, ?)
ORDER BY CASE status
    WHEN ? THEN 0
    WHEN ? THEN 1
    ELSE 2
END, created_at`,
		JobRunning, JobQueued, JobWaitingInput,
		JobRunning, JobWaitingInput,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) CountJobs(ctx context.Context, status string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM jobs WHERE archived_at IS NULL AND status = ?", status,
	).Scan(&count)
	return count, err
}

func (s *Store) ClaimNextJob(ctx context.Context) (Job, bool, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer transaction.Rollback()

	job, err := scanJob(transaction.QueryRowContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE archived_at IS NULL AND status = ? ORDER BY created_at LIMIT 1",
		JobQueued))
	if isNotFound(err) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}

	started := time.Now().UTC()
	result, err := transaction.ExecContext(ctx, `
UPDATE jobs SET status = ?, started_at = ?, finished_at = NULL, error = ''
WHERE id = ? AND status = ?`, JobRunning, started.Format(time.RFC3339Nano), job.ID, JobQueued)
	if err != nil {
		return Job{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return Job{}, false, err
	}
	if _, err := appendEventTx(ctx, transaction, job.ID, "status", JobRunning); err != nil {
		return Job{}, false, err
	}
	if err := transaction.Commit(); err != nil {
		return Job{}, false, err
	}
	job.Status = JobRunning
	job.StartedAt = &started
	return job, true, nil
}

func (s *Store) FinishJob(ctx context.Context, id, status, resultText, errorText string) error {
	if status != JobCompleted && status != JobFailed &&
		status != JobCancelled {
		return fmt.Errorf("invalid terminal job status %q", status)
	}
	finished := nowText()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `
UPDATE jobs SET status = ?, result = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, resultText, errorText, finished, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("job %q not found", id)
	}
	if _, err := appendEventTx(ctx, transaction, id, "status", status); err != nil {
		return err
	}
	return transaction.Commit()
}

func (s *Store) RequestCancel(ctx context.Context, id string) (Job, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer transaction.Rollback()
	job, err := scanJob(transaction.QueryRowContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE id = ?", id))
	if isNotFound(err) {
		return Job{}, fmt.Errorf("job %q not found", id)
	}
	if err != nil {
		return Job{}, err
	}
	if err := rejectArchivedJob(job); err != nil {
		return Job{}, err
	}
	if job.Terminal() {
		return job, nil
	}

	if job.Status == JobQueued || job.Status == JobWaitingInput {
		finished := time.Now().UTC()
		_, err = transaction.ExecContext(ctx, `
UPDATE jobs SET status = ?, cancel_requested = 1, finished_at = ?,
input_question = '', input_tool_call_id = '', input_response = '' WHERE id = ?`,
			JobCancelled, finished.Format(time.RFC3339Nano), id)
		job.Status = JobCancelled
		job.FinishedAt = &finished
		job.Question = ""
		job.InputToolCallID = ""
		job.InputResponse = ""
		if err == nil {
			_, err = appendEventTx(ctx, transaction, id, "status", JobCancelled)
		}
	} else {
		_, err = transaction.ExecContext(ctx,
			"UPDATE jobs SET cancel_requested = 1 WHERE id = ?", id)
		job.CancelRequested = true
		if err == nil {
			_, err = appendEventTx(ctx, transaction, id, "status", "cancelling")
		}
	}
	if err != nil {
		return Job{}, err
	}
	return job, transaction.Commit()
}

func (s *Store) RetryJob(ctx context.Context, id string) (Job, int64, error) {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return Job{}, 0, err
	}
	if err := rejectArchivedJob(job); err != nil {
		return Job{}, 0, err
	}
	if job.Status != JobCompleted && job.Status != JobFailed && job.Status != JobInterrupted &&
		job.Status != JobCancelled {
		return Job{}, 0, fmt.Errorf("job %q cannot be retried from status %q", id, job.Status)
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, 0, err
	}
	defer transaction.Rollback()
	_, err = transaction.ExecContext(ctx, `
UPDATE jobs SET status = ?, error = '', result = '', cancel_requested = 0,
started_at = NULL, finished_at = NULL WHERE id = ? AND archived_at IS NULL`, JobQueued, id)
	var retrySequence int64
	if err == nil {
		retrySequence, err = appendEventTx(ctx, transaction, id, "status", JobQueued)
	}
	if err != nil {
		return Job{}, 0, err
	}
	if err := transaction.Commit(); err != nil {
		return Job{}, 0, err
	}
	job.Status = JobQueued
	job.Error = ""
	job.Result = ""
	job.CancelRequested = false
	job.StartedAt = nil
	job.FinishedAt = nil
	return job, retrySequence, nil
}

func (s *Store) PauseForInput(ctx context.Context, id, toolCallID, question string) error {
	toolCallID = strings.TrimSpace(toolCallID)
	question = strings.TrimSpace(question)
	if toolCallID == "" {
		return errors.New("user input request is missing a tool call ID")
	}
	if question == "" {
		return errors.New("user input question cannot be empty")
	}
	if len(question) > 16<<10 {
		return errors.New("user input question is too long")
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `
UPDATE jobs SET status = ?, input_question = ?, input_tool_call_id = ?,
input_response = '', error = '', result = '', cancel_requested = 0,
finished_at = NULL WHERE id = ? AND status = ?`,
		JobWaitingInput, question, toolCallID, id, JobRunning)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("job %q is not running and cannot request input", id)
	}
	if _, err := appendEventTx(ctx, transaction, id, "input_required", question); err != nil {
		return err
	}
	if _, err := appendEventTx(ctx, transaction, id, "status", JobWaitingInput); err != nil {
		return err
	}
	return transaction.Commit()
}

func (s *Store) RespondToJob(ctx context.Context, id, response string) (Job, int64, error) {
	response = strings.TrimSpace(response)
	if response == "" {
		return Job{}, 0, errors.New("response cannot be empty")
	}
	if len(response) > 1<<20 {
		return Job{}, 0, errors.New("response is too long")
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, 0, err
	}
	defer transaction.Rollback()
	job, err := scanJob(transaction.QueryRowContext(ctx,
		"SELECT "+jobColumns+" FROM jobs WHERE id = ?", id))
	if isNotFound(err) {
		return Job{}, 0, fmt.Errorf("job %q not found", id)
	}
	if err != nil {
		return Job{}, 0, err
	}
	if err := rejectArchivedJob(job); err != nil {
		return Job{}, 0, err
	}
	if job.Status != JobWaitingInput {
		return Job{}, 0, fmt.Errorf("job %q is not waiting for input", id)
	}
	if job.InputToolCallID == "" {
		return Job{}, 0, fmt.Errorf("job %q has no pending input request", id)
	}

	result, err := transaction.ExecContext(ctx, `
UPDATE jobs SET status = ?, input_question = '', input_response = ?,
cancel_requested = 0, started_at = NULL, finished_at = NULL
WHERE id = ? AND status = ? AND archived_at IS NULL`, JobQueued, response, id, JobWaitingInput)
	if err != nil {
		return Job{}, 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Job{}, 0, err
	}
	if affected != 1 {
		return Job{}, 0, fmt.Errorf("job %q is no longer waiting for input", id)
	}
	if _, err := appendEventTx(ctx, transaction, id, "user_input", response); err != nil {
		return Job{}, 0, err
	}
	sequence, err := appendEventTx(ctx, transaction, id, "status", JobQueued)
	if err != nil {
		return Job{}, 0, err
	}
	if err := transaction.Commit(); err != nil {
		return Job{}, 0, err
	}
	job.Status = JobQueued
	job.Question = ""
	job.InputResponse = response
	job.CancelRequested = false
	job.StartedAt = nil
	job.FinishedAt = nil
	return job, sequence, nil
}

func (s *Store) LoadUserInputResponse(
	ctx context.Context,
	jobID, toolCallID string,
) (string, bool, error) {
	var storedToolCallID, response string
	err := s.db.QueryRowContext(ctx, `
SELECT input_tool_call_id, input_response FROM jobs WHERE id = ?`, jobID).Scan(
		&storedToolCallID, &response,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("job %q not found", jobID)
	}
	if err != nil {
		return "", false, err
	}
	if storedToolCallID != toolCallID || response == "" {
		return "", false, nil
	}
	return response, true, nil
}

func (s *Store) RecoverRunningJobs(ctx context.Context) (int64, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id FROM jobs WHERE archived_at IS NULL AND status = ?", JobRunning,
	)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer transaction.Rollback()
	finished := nowText()
	for _, id := range ids {
		if _, err := transaction.ExecContext(ctx, `
UPDATE jobs SET status = ?, error = ?, finished_at = ? WHERE id = ? AND status = ?`,
			JobInterrupted, "Pearl stopped while this job was running", finished, id, JobRunning); err != nil {
			return 0, err
		}
		if _, err := appendEventTx(ctx, transaction, id, "status", JobInterrupted); err != nil {
			return 0, err
		}
	}
	return int64(len(ids)), transaction.Commit()
}

func rejectArchivedJob(job Job) error {
	if job.ArchivedAt != nil {
		return fmt.Errorf("job %q is archived", job.ID)
	}
	return nil
}

func appendEventTx(ctx context.Context, transaction *sql.Tx, jobID, eventType, data string) (int64, error) {
	result, err := transaction.ExecContext(ctx, `
INSERT INTO events(job_id, type, data, created_at) VALUES (?, ?, ?, ?)`,
		jobID, eventType, data, nowText())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) AppendEvent(ctx context.Context, jobID, eventType, data string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
INSERT INTO events(job_id, type, data, created_at) VALUES (?, ?, ?, ?)`,
		jobID, eventType, data, nowText())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) EventsAfter(ctx context.Context, jobID string, sequence int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT sequence, job_id, type, data, created_at FROM events
WHERE job_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`, jobID, sequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		var created string
		if err := rows.Scan(&event.Sequence, &event.JobID, &event.Type, &event.Data, &created); err != nil {
			return nil, err
		}
		event.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) LoadTranscript(ctx context.Context, jobID string) ([]byte, error) {
	var transcript []byte
	err := s.db.QueryRowContext(ctx, "SELECT transcript FROM jobs WHERE id = ?", jobID).Scan(&transcript)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("job %q not found", jobID)
	}
	return transcript, err
}

func (s *Store) SaveTranscript(ctx context.Context, jobID string, transcript []byte) error {
	_, err := s.db.ExecContext(ctx, "UPDATE jobs SET transcript = ? WHERE id = ?", transcript, jobID)
	return err
}

func (s *Store) LoadToolResult(ctx context.Context, jobID, toolCallID string) ([]byte, bool, error) {
	var result string
	err := s.db.QueryRowContext(ctx, `
SELECT result FROM tool_executions WHERE job_id = ? AND tool_call_id = ?`,
		jobID, toolCallID).Scan(&result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return []byte(result), err == nil, err
}

func (s *Store) SaveToolResult(
	ctx context.Context,
	jobID, toolCallID, toolName, arguments string,
	result []byte,
) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO tool_executions(job_id, tool_call_id, tool_name, arguments, result, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(job_id, tool_call_id) DO NOTHING`,
		jobID, toolCallID, toolName, arguments, string(result), nowText())
	return err
}

func (s *Store) ListToolExecutions(ctx context.Context, jobID string) ([]ToolExecution, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT job_id, tool_call_id, tool_name, arguments, result, created_at
FROM tool_executions
WHERE job_id = ?
ORDER BY created_at, rowid`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []ToolExecution
	for rows.Next() {
		var execution ToolExecution
		var created string
		if err := rows.Scan(
			&execution.JobID,
			&execution.ToolCallID,
			&execution.ToolName,
			&execution.Arguments,
			&execution.Result,
			&created,
		); err != nil {
			return nil, err
		}
		execution.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}
