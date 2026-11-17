package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
	"github.com/August-H/pearl-cli/internal/workspacechanges"
)

func (m *Manager) beginChanges(ctx context.Context, job store.Job) (store.OpenChangeRun, error) {
	capture, cancel := context.WithTimeout(ctx, 20*time.Second)
	before, err := workspacechanges.Capture(capture, job.WorkspaceRoot, nil)
	cancel()
	run := store.OpenChangeRun{WorkspaceRoot: job.WorkspaceRoot, Report: workspacechanges.Report{Changes: []workspacechanges.Change{}}}
	if err != nil {
		run.Report.Notes = append(run.Report.Notes, "Could not capture starting files: "+err.Error())
	} else {
		run.Baseline = &before
	}
	write, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run.ID, err = m.store.BeginChangeRun(write, job.ID, run.Baseline, run.Report)
	return run, err
}

func finishChanges(state *store.Store, run store.OpenChangeRun, recovered bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	report := run.Report
	if run.Baseline != nil {
		after, err := workspacechanges.Capture(ctx, run.WorkspaceRoot, run.Baseline)
		if err != nil {
			report.Notes = append(report.Notes, "Could not capture final files: "+err.Error()+"; actual changes are unavailable.")
		} else {
			report = workspacechanges.Compare(*run.Baseline, after)
		}
	}
	if recovered {
		report.Notes = append(report.Notes, "Captured after daemon recovery; changes made while Pearl was stopped may be included.")
	}
	write, writeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer writeCancel()
	return state.CompleteChangeRun(write, run.ID, report)
}

// Recover snapshots before workers start, while no other Pearl job can edit the
// workspace. Do not silently discard a report that could not be persisted.
func recoverChanges(ctx context.Context, state *store.Store) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		run, found, err := state.NextOpenChangeRun(ctx)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if err := finishChanges(state, run, true); err != nil {
			return fmt.Errorf("recover change capture %d: %w", run.ID, err)
		}
	}
}
