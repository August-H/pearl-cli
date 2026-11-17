package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/August-H/pearl-cli/internal/workspacechanges"
)

// JobChangeRun records one execution, including retries and follow-ups. Completed
// reports are immutable so intervening jobs' edits cannot enter a later diff.
type JobChangeRun struct {
	ID         int64                   `json:"id"`
	StartedAt  time.Time               `json:"started_at"`
	FinishedAt *time.Time              `json:"finished_at,omitempty"`
	Report     workspacechanges.Report `json:"report"`
}

func (s *Store) BeginChangeRun(ctx context.Context, jobID string, baseline *workspacechanges.Snapshot, report workspacechanges.Report) (int64, error) {
	var saved []byte
	var err error
	if baseline != nil {
		saved, err = json.Marshal(baseline)
		if err != nil {
			return 0, err
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO job_change_runs(job_id, baseline, report, started_at) VALUES (?, ?, ?, ?)`, jobID, saved, encoded, nowText())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) CompleteChangeRun(ctx context.Context, id int64, report workspacechanges.Report) error {
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE job_change_runs SET baseline = NULL, report = ?, finished_at = ? WHERE id = ? AND finished_at IS NULL`, encoded, nowText(), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return fmt.Errorf("change capture %d is missing or already finished", id)
	}
	return err
}

func (s *Store) ListChangeRuns(ctx context.Context, jobID string) ([]JobChangeRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, started_at, finished_at, report FROM job_change_runs WHERE job_id = ? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []JobChangeRun
	for rows.Next() {
		var run JobChangeRun
		var started string
		var finished sql.NullString
		var report []byte
		if err := rows.Scan(&run.ID, &started, &finished, &report); err != nil {
			return nil, err
		}
		run.StartedAt, err = parseTime(started)
		if err != nil {
			return nil, err
		}
		run.FinishedAt, err = parseOptionalTime(finished)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(report, &run.Report); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

type OpenChangeRun struct {
	ID            int64
	WorkspaceRoot string
	Baseline      *workspacechanges.Snapshot
	Report        workspacechanges.Report
}

// NextOpenChangeRun loads just one baseline, keeping recovery memory bounded
// even if many sessions were interrupted together.
func (s *Store) NextOpenChangeRun(ctx context.Context) (OpenChangeRun, bool, error) {
	var run OpenChangeRun
	var baseline, report []byte
	err := s.db.QueryRowContext(ctx, `SELECT capture.id, jobs.workspace_root, capture.baseline, capture.report
FROM job_change_runs AS capture JOIN jobs ON jobs.id = capture.job_id
WHERE capture.finished_at IS NULL ORDER BY capture.id LIMIT 1`).Scan(&run.ID, &run.WorkspaceRoot, &baseline, &report)
	if err == sql.ErrNoRows {
		return run, false, nil
	}
	if err != nil {
		return run, false, err
	}
	if len(baseline) > 0 {
		if err := json.Unmarshal(baseline, &run.Baseline); err != nil {
			return run, false, err
		}
	}
	if err := json.Unmarshal(report, &run.Report); err != nil {
		return run, false, err
	}
	return run, true, nil
}
