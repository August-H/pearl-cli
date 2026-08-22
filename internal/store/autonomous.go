package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) CreateAutonomousSession(
	ctx context.Context,
	goal, workspaceRoot string,
) (AutonomousSession, error) {
	goal = strings.TrimSpace(goal)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if goal == "" {
		return AutonomousSession{}, errors.New("goal cannot be empty")
	}
	if workspaceRoot == "" {
		return AutonomousSession{}, errors.New("workspace root cannot be empty")
	}
	now := time.Now().UTC()
	session := AutonomousSession{
		ID:            newID("auto"),
		Goal:          goal,
		WorkspaceRoot: workspaceRoot,
		Status:        AutonomousRunning,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO autonomous_sessions (
    id, goal, workspace_root, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?)`,
		session.ID, session.Goal, session.WorkspaceRoot, session.Status,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	return session, err
}

func (s *Store) GetAutonomousSession(
	ctx context.Context,
	id string,
) (AutonomousSession, error) {
	session, err := scanAutonomousSession(s.db.QueryRowContext(ctx, `
SELECT id, goal, workspace_root, status, summary, error,
created_at, updated_at, finished_at
FROM autonomous_sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AutonomousSession{}, fmt.Errorf("autonomous session %q not found", id)
	}
	return session, err
}

func (s *Store) LatestAutonomousSession(
	ctx context.Context,
) (AutonomousSession, error) {
	session, err := scanAutonomousSession(s.db.QueryRowContext(ctx, `
SELECT id, goal, workspace_root, status, summary, error,
created_at, updated_at, finished_at
FROM autonomous_sessions ORDER BY created_at DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return AutonomousSession{}, errors.New("no autonomous sessions found")
	}
	return session, err
}

func (s *Store) ListOpenAutonomousSessions(
	ctx context.Context,
) ([]AutonomousSession, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, goal, workspace_root, status, summary, error,
created_at, updated_at, finished_at
FROM autonomous_sessions
WHERE status IN (?, ?, ?)
ORDER BY created_at`, AutonomousRunning, AutonomousReviewing, AutonomousWaitingInput)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []AutonomousSession
	for rows.Next() {
		session, err := scanAutonomousSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) AutonomousDetails(
	ctx context.Context,
	id string,
) (AutonomousDetails, error) {
	session, err := s.GetAutonomousSession(ctx, id)
	if err != nil {
		return AutonomousDetails{}, err
	}
	jobs, err := s.ListAutonomousJobs(ctx, id)
	if err != nil {
		return AutonomousDetails{}, err
	}
	events, err := s.AutonomousStatusEvents(ctx, id)
	if err != nil {
		return AutonomousDetails{}, err
	}
	return AutonomousDetails{Session: session, Jobs: jobs, Events: events}, nil
}

func (s *Store) CreateAutonomousJob(
	ctx context.Context,
	sessionID, prompt string,
) (Job, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Job{}, errors.New("job prompt cannot be empty")
	}
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer transaction.Rollback()

	var workspaceRoot, sessionStatus string
	if err := transaction.QueryRowContext(ctx, `
SELECT workspace_root, status FROM autonomous_sessions WHERE id = ?`,
		sessionID).Scan(&workspaceRoot, &sessionStatus); errors.Is(err, sql.ErrNoRows) {
		return Job{}, fmt.Errorf("autonomous session %q not found", sessionID)
	} else if err != nil {
		return Job{}, err
	}
	if autonomousStatusTerminal(sessionStatus) {
		return Job{}, fmt.Errorf(
			"autonomous session %q is already %s", sessionID, sessionStatus,
		)
	}

	created := time.Now().UTC()
	job := Job{
		Prompt: prompt, WorkspaceRoot: workspaceRoot,
		Status: JobQueued, CreatedAt: created,
	}
	job.ID, err = availableJobID(ctx, transaction)
	if err != nil {
		return Job{}, err
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO jobs(id, prompt, workspace_root, status, created_at)
VALUES (?, ?, ?, ?, ?)`, job.ID, job.Prompt, job.WorkspaceRoot, job.Status,
		created.Format(time.RFC3339Nano)); err != nil {
		return Job{}, err
	}
	if _, err := appendEventTx(ctx, transaction, job.ID, "status", job.Status); err != nil {
		return Job{}, err
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO autonomous_jobs(session_id, job_id, created_at)
VALUES (?, ?, ?)`, sessionID, job.ID, created.Format(time.RFC3339Nano)); err != nil {
		return Job{}, err
	}
	if _, err := transaction.ExecContext(ctx, `
UPDATE autonomous_sessions
SET status = ?, error = '', updated_at = ?, finished_at = NULL
WHERE id = ?`, AutonomousRunning, created.Format(time.RFC3339Nano), sessionID); err != nil {
		return Job{}, err
	}
	return job, transaction.Commit()
}

func (s *Store) ListAutonomousJobs(
	ctx context.Context,
	sessionID string,
) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+jobColumns+` FROM jobs
WHERE id IN (
    SELECT job_id FROM autonomous_jobs WHERE session_id = ?
)
ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

func (s *Store) AutonomousStatusEvents(
	ctx context.Context,
	sessionID string,
) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT events.sequence, events.job_id, events.type, events.data, events.created_at
FROM events
JOIN autonomous_jobs ON autonomous_jobs.job_id = events.job_id
WHERE autonomous_jobs.session_id = ? AND events.type = 'status'
ORDER BY events.sequence`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		var created string
		if err := rows.Scan(
			&event.Sequence, &event.JobID, &event.Type, &event.Data, &created,
		); err != nil {
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

func (s *Store) UpdateAutonomousSession(
	ctx context.Context,
	id, status, summary, errorText string,
) error {
	if !validAutonomousStatus(status) {
		return fmt.Errorf("invalid autonomous status %q", status)
	}
	now := time.Now().UTC()
	var finished any
	if autonomousStatusTerminal(status) {
		finished = now.Format(time.RFC3339Nano)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE autonomous_sessions
SET status = ?, summary = ?, error = ?, updated_at = ?, finished_at = ?
WHERE id = ?`, status, strings.TrimSpace(summary), strings.TrimSpace(errorText),
		now.Format(time.RFC3339Nano), finished, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("autonomous session %q not found", id)
	}
	return nil
}

func scanAutonomousSession(
	scanner interface{ Scan(...any) error },
) (AutonomousSession, error) {
	var session AutonomousSession
	var created, updated string
	var finished sql.NullString
	err := scanner.Scan(
		&session.ID, &session.Goal, &session.WorkspaceRoot, &session.Status,
		&session.Summary, &session.Error, &created, &updated, &finished,
	)
	if err != nil {
		return AutonomousSession{}, err
	}
	session.CreatedAt, err = parseTime(created)
	if err != nil {
		return AutonomousSession{}, err
	}
	session.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return AutonomousSession{}, err
	}
	session.FinishedAt, err = parseOptionalTime(finished)
	return session, err
}

func validAutonomousStatus(status string) bool {
	switch status {
	case AutonomousRunning, AutonomousReviewing, AutonomousWaitingInput,
		AutonomousCompleted, AutonomousFailed, AutonomousCancelled:
		return true
	default:
		return false
	}
}

func autonomousStatusTerminal(status string) bool {
	return status == AutonomousCompleted || status == AutonomousFailed ||
		status == AutonomousCancelled
}
