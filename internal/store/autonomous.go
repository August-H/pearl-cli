package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const autonomousSessionColumns = `id, goal, workspace_root, status, summary, error,
orchestrator_provider, orchestrator_model, orchestrator_name, allowed_models, all_models,
max_subagents, plan, created_at, updated_at, finished_at`

func (s *Store) CreateAutonomousSession(
	ctx context.Context,
	config AutonomousConfig,
) (AutonomousSession, error) {
	goal := strings.TrimSpace(config.Goal)
	workspaceRoot := strings.TrimSpace(config.WorkspaceRoot)
	if goal == "" {
		return AutonomousSession{}, errors.New("goal cannot be empty")
	}
	if workspaceRoot == "" {
		return AutonomousSession{}, errors.New("workspace root cannot be empty")
	}
	allowed, err := json.Marshal(config.AllowedModels)
	if err != nil {
		return AutonomousSession{}, err
	}
	now := time.Now().UTC()
	session := AutonomousSession{
		ID:            newID("auto"),
		Goal:          goal,
		WorkspaceRoot: workspaceRoot,
		Status:        AutonomousRunning,
		Orchestrator:  config.Orchestrator,
		AllowedModels: config.AllowedModels,
		AllModels:     config.AllModels,
		MaxSubagents:  max(1, config.MaxSubagents),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO autonomous_sessions (
    id, goal, workspace_root, status, orchestrator_provider, orchestrator_model,
    orchestrator_name, allowed_models, all_models, max_subagents, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.Goal, session.WorkspaceRoot, session.Status,
		session.Orchestrator.Provider, session.Orchestrator.ID, session.Orchestrator.Name,
		string(allowed), session.AllModels, session.MaxSubagents,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	return session, err
}

// SetAutonomousPlan saves the orchestrator's notes, which it reads back each turn.
func (s *Store) SetAutonomousPlan(ctx context.Context, id, plan string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE autonomous_sessions SET plan = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(plan), nowText(), id)
	return err
}

func (s *Store) GetAutonomousSession(
	ctx context.Context,
	id string,
) (AutonomousSession, error) {
	session, err := scanAutonomousSession(s.db.QueryRowContext(ctx,
		"SELECT "+autonomousSessionColumns+" FROM autonomous_sessions WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return AutonomousSession{}, fmt.Errorf("autonomous session %q not found", id)
	}
	return session, err
}

func (s *Store) LatestAutonomousSession(
	ctx context.Context,
) (AutonomousSession, error) {
	session, err := scanAutonomousSession(s.db.QueryRowContext(ctx,
		"SELECT "+autonomousSessionColumns+" FROM autonomous_sessions ORDER BY created_at DESC LIMIT 1"))
	if errors.Is(err, sql.ErrNoRows) {
		return AutonomousSession{}, errors.New("no autonomous sessions found")
	}
	return session, err
}

func (s *Store) ListOpenAutonomousSessions(
	ctx context.Context,
) ([]AutonomousSession, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+autonomousSessionColumns+` FROM autonomous_sessions
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
	turns, err := s.ListAutonomousTurns(ctx, id)
	if err != nil {
		return AutonomousDetails{}, err
	}
	activity, err := s.autonomousJobActivity(ctx, id)
	if err != nil {
		return AutonomousDetails{}, err
	}
	return AutonomousDetails{Session: session, Jobs: jobs, Events: events, Turns: turns, JobActivity: activity}, nil
}

// StartAutonomousTurn records that the orchestrator is being asked again. Any turn
// still open was cut off, as when the daemon stopped partway through it.
func (s *Store) StartAutonomousTurn(ctx context.Context, sessionID, trigger string) (int, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer transaction.Rollback()
	now := nowText()
	if _, err := transaction.ExecContext(ctx, `
UPDATE autonomous_turns SET finished_at = ?, outcome = 'Cut off before it finished'
WHERE session_id = ? AND finished_at IS NULL`, now, sessionID); err != nil {
		return 0, err
	}
	var turn int
	if err := transaction.QueryRowContext(ctx, `
SELECT COALESCE(MAX(turn), 0) + 1 FROM autonomous_turns WHERE session_id = ?`, sessionID).Scan(&turn); err != nil {
		return 0, err
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO autonomous_turns(session_id, turn, trigger_text, started_at) VALUES (?, ?, ?, ?)`,
		sessionID, turn, trigger, now); err != nil {
		return 0, err
	}
	return turn, transaction.Commit()
}

// UpdateAutonomousTurn saves what the orchestrator has said so far.
func (s *Store) UpdateAutonomousTurn(ctx context.Context, sessionID string, turn int, reasoning string, decisionBytes int) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE autonomous_turns SET reasoning = ?, decision_bytes = ?, updated_at = ? WHERE session_id = ? AND turn = ?`,
		reasoning, decisionBytes, nowText(), sessionID, turn)
	return err
}

func (s *Store) FinishAutonomousTurn(ctx context.Context, sessionID string, turn int, outcome string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE autonomous_turns SET outcome = ?, finished_at = ? WHERE session_id = ? AND turn = ?`,
		outcome, nowText(), sessionID, turn)
	return err
}

// Screens poll for these, so very old turns and very long reasoning are left out.
const (
	autonomousTurnsShown     = 60
	autonomousReasoningShown = 64 << 10
)

func (s *Store) ListAutonomousTurns(ctx context.Context, sessionID string) ([]AutonomousTurn, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT turn, trigger_text, substr(reasoning, -?), decision_bytes, outcome, updated_at, started_at, finished_at
FROM autonomous_turns WHERE session_id = ? ORDER BY turn DESC LIMIT ?`,
		autonomousReasoningShown, sessionID, autonomousTurnsShown)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []AutonomousTurn
	for rows.Next() {
		var turn AutonomousTurn
		var started string
		var updated, finished sql.NullString
		if err := rows.Scan(&turn.Turn, &turn.Trigger, &turn.Reasoning, &turn.DecisionBytes, &turn.Outcome, &updated, &started, &finished); err != nil {
			return nil, err
		}
		if turn.UpdatedAt, err = parseOptionalTime(updated); err != nil {
			return nil, err
		}
		if turn.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		if turn.FinishedAt, err = parseOptionalTime(finished); err != nil {
			return nil, err
		}
		turns = append(turns, turn)
	}
	slices.Reverse(turns)
	return turns, rows.Err()
}

// autonomousJobActivity finds each job's latest step. Some harnesses stream their
// words a few at a time, and those fragments say nothing on their own.
func (s *Store) autonomousJobActivity(ctx context.Context, sessionID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT latest.job_id, events.type, substr(events.data, 1, 400) FROM (
    SELECT events.job_id, MAX(events.sequence) AS sequence FROM events
    JOIN autonomous_jobs ON autonomous_jobs.job_id = events.job_id
    WHERE autonomous_jobs.session_id = ?
      AND (events.type = 'tool' OR (events.type IN ('reasoning', 'answer') AND length(events.data) >= 24))
    GROUP BY events.job_id
) AS latest
JOIN events ON events.sequence = latest.sequence`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	activity := map[string]string{}
	for rows.Next() {
		var job, kind, data string
		if err := rows.Scan(&job, &kind, &data); err != nil {
			return nil, err
		}
		if kind == "reasoning" {
			data = "thinking: " + data
		}
		activity[job] = data
	}
	return activity, rows.Err()
}

func (s *Store) CreateAutonomousJob(
	ctx context.Context,
	sessionID, prompt, provider, model string,
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
		AssignedProvider: provider, AssignedModel: model,
	}
	job.ID, err = availableJobID(ctx, transaction)
	if err != nil {
		return Job{}, err
	}
	if _, err := transaction.ExecContext(ctx, `
INSERT INTO jobs(id, prompt, workspace_root, status, created_at, assigned_provider, assigned_model)
VALUES (?, ?, ?, ?, ?, ?, ?)`, job.ID, job.Prompt, job.WorkspaceRoot, job.Status,
		created.Format(time.RFC3339Nano), provider, model); err != nil {
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
WHERE id = ? AND status <> ?`, status, strings.TrimSpace(summary), strings.TrimSpace(errorText),
		now.Format(time.RFC3339Nano), finished, id, AutonomousCancelled)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		// A stopped session stays stopped, whatever its last turn was doing.
		_, err := s.GetAutonomousSession(ctx, id)
		return err
	}
	return nil
}

func scanAutonomousSession(
	scanner interface{ Scan(...any) error },
) (AutonomousSession, error) {
	var session AutonomousSession
	var created, updated, allowed string
	var finished sql.NullString
	err := scanner.Scan(
		&session.ID, &session.Goal, &session.WorkspaceRoot, &session.Status,
		&session.Summary, &session.Error,
		&session.Orchestrator.Provider, &session.Orchestrator.ID, &session.Orchestrator.Name,
		&allowed, &session.AllModels, &session.MaxSubagents, &session.Plan,
		&created, &updated, &finished,
	)
	if err != nil {
		return AutonomousSession{}, err
	}
	if allowed != "" {
		if err := json.Unmarshal([]byte(allowed), &session.AllowedModels); err != nil {
			return AutonomousSession{}, fmt.Errorf("read allowed models: %w", err)
		}
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
