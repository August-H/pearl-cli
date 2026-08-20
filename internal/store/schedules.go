package store

import (
	"context"
	"fmt"
	"time"
)

func (s *Store) CreateSchedule(
	ctx context.Context,
	name, prompt, workspaceRoot string,
	interval time.Duration,
) (Schedule, error) {
	if interval < time.Second {
		return Schedule{}, fmt.Errorf("schedule interval must be at least one second")
	}
	now := time.Now().UTC()
	schedule := Schedule{
		ID:              newID("schedule"),
		Name:            name,
		Prompt:          prompt,
		WorkspaceRoot:   workspaceRoot,
		IntervalSeconds: int64(interval / time.Second),
		NextRunAt:       now.Add(interval),
		Enabled:         true,
		CreatedAt:       now,
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO schedules(id, name, prompt, workspace_root, interval_seconds,
next_run_at, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
		schedule.ID, schedule.Name, schedule.Prompt, schedule.WorkspaceRoot,
		schedule.IntervalSeconds, schedule.NextRunAt.Format(time.RFC3339Nano),
		schedule.CreatedAt.Format(time.RFC3339Nano))
	return schedule, err
}

func scanSchedule(scanner interface{ Scan(...any) error }) (Schedule, error) {
	var schedule Schedule
	var nextRun, created string
	var enabled int
	err := scanner.Scan(&schedule.ID, &schedule.Name, &schedule.Prompt,
		&schedule.WorkspaceRoot, &schedule.IntervalSeconds, &nextRun,
		&enabled, &created)
	if err != nil {
		return Schedule{}, err
	}
	schedule.Enabled = enabled != 0
	schedule.NextRunAt, err = parseTime(nextRun)
	if err != nil {
		return Schedule{}, err
	}
	schedule.CreatedAt, err = parseTime(created)
	return schedule, err
}

const scheduleColumns = `id, name, prompt, workspace_root, interval_seconds,
next_run_at, enabled, created_at`

func (s *Store) ListSchedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+scheduleColumns+" FROM schedules ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var schedules []Schedule
	for rows.Next() {
		schedule, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM schedules WHERE id = ?", id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("schedule %q not found", id)
	}
	return nil
}

func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+scheduleColumns+` FROM schedules
WHERE enabled = 1 AND next_run_at <= ? ORDER BY next_run_at`,
		now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var schedules []Schedule
	for rows.Next() {
		schedule, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (s *Store) FireSchedule(ctx context.Context, schedule Schedule, now time.Time) (Job, bool, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer transaction.Rollback()

	nextRun := schedule.NextRunAt
	interval := time.Duration(schedule.IntervalSeconds) * time.Second
	for !nextRun.After(now) {
		nextRun = nextRun.Add(interval)
	}
	result, err := transaction.ExecContext(ctx, `
UPDATE schedules SET next_run_at = ? WHERE id = ? AND enabled = 1 AND next_run_at = ?`,
		nextRun.Format(time.RFC3339Nano), schedule.ID,
		schedule.NextRunAt.Format(time.RFC3339Nano))
	if err != nil {
		return Job{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return Job{}, false, err
	}

	job := Job{
		Prompt:        schedule.Prompt,
		WorkspaceRoot: schedule.WorkspaceRoot,
		Status:        JobQueued,
		CreatedAt:     now.UTC(),
	}
	job.ID, err = availableJobID(ctx, transaction)
	if err != nil {
		return Job{}, false, err
	}
	_, err = transaction.ExecContext(ctx, `
INSERT INTO jobs(id, prompt, workspace_root, status, created_at)
VALUES (?, ?, ?, ?, ?)`, job.ID, job.Prompt, job.WorkspaceRoot, job.Status,
		job.CreatedAt.Format(time.RFC3339Nano))
	if err == nil {
		_, err = appendEventTx(ctx, transaction, job.ID, "schedule", schedule.ID)
	}
	if err == nil {
		_, err = appendEventTx(ctx, transaction, job.ID, "status", JobQueued)
	}
	if err != nil {
		return Job{}, false, err
	}
	return job, true, transaction.Commit()
}
