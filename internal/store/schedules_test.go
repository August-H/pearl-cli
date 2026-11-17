package store

import (
	"context"
	"testing"
	"time"
)

func TestScheduleSkipsOutstandingRunsAndKeepsCadence(t *testing.T) {
	for _, status := range []string{JobQueued, JobRunning, JobWaitingInput} {
		t.Run(status, func(t *testing.T) {
			state := openTestStore(t)
			ctx := context.Background()
			schedule, err := state.CreateSchedule(ctx, "check", "inspect", t.TempDir(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			job, fired, err := state.FireSchedule(ctx, schedule, schedule.NextRunAt)
			if err != nil || !fired {
				t.Fatalf("initial fire = %v, %v", fired, err)
			}
			if _, err := state.db.ExecContext(ctx, "UPDATE jobs SET status = ? WHERE id = ?", status, job.ID); err != nil {
				t.Fatal(err)
			}
			listed, err := state.ListSchedules(ctx)
			if err != nil {
				t.Fatal(err)
			}
			next := listed[0]
			now := next.NextRunAt.Add(9*time.Minute + 15*time.Second)
			if _, fired, err := state.FireSchedule(ctx, next, now); err != nil || fired {
				t.Fatalf("overlapping fire = %v, %v", fired, err)
			}
			listed, err = state.ListSchedules(ctx)
			if err != nil {
				t.Fatal(err)
			}
			advanced := listed[0]
			if !advanced.NextRunAt.Equal(now.Add(45 * time.Second)) {
				t.Fatalf("cadence moved to %s", advanced.NextRunAt)
			}
			jobs, err := state.ListJobs(ctx, 100)
			if err != nil || len(jobs) != 1 {
				t.Fatalf("backlog = %+v, %v", jobs, err)
			}
			if _, err := state.db.ExecContext(ctx, "UPDATE jobs SET status = ? WHERE id = ?", JobCompleted, job.ID); err != nil {
				t.Fatal(err)
			}
			if _, fired, err := state.FireSchedule(ctx, advanced, now); err != nil || fired {
				t.Fatalf("early fire = %v, %v", fired, err)
			}
			if _, fired, err := state.FireSchedule(ctx, advanced, advanced.NextRunAt); err != nil || !fired {
				t.Fatalf("next fire = %v, %v", fired, err)
			}
			if _, fired, err := state.FireSchedule(ctx, advanced, advanced.NextRunAt); err != nil || fired {
				t.Fatalf("stale fire = %v, %v", fired, err)
			}
		})
	}
}

func TestScheduleDeduplicationDoesNotBlockOtherSchedulesOrTerminalRuns(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	workspace := t.TempDir()
	a, err := state.CreateSchedule(ctx, "a", "inspect", workspace, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := state.CreateSchedule(ctx, "b", "inspect", workspace, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	first, fired, err := state.FireSchedule(ctx, a, a.NextRunAt)
	if err != nil || !fired {
		t.Fatalf("a fire = %v, %v", fired, err)
	}
	if _, fired, err := state.FireSchedule(ctx, b, b.NextRunAt); err != nil || !fired {
		t.Fatalf("b fire = %v, %v", fired, err)
	}
	for _, status := range []string{JobCompleted, JobFailed, JobCancelled, JobInterrupted} {
		if _, err := state.db.ExecContext(ctx, "UPDATE jobs SET status = ? WHERE id = ?", status, first.ID); err != nil {
			t.Fatal(err)
		}
		listed, err := state.ListSchedules(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, schedule := range listed {
			if schedule.ID == a.ID {
				a = schedule
			}
		}
		first, fired, err = state.FireSchedule(ctx, a, a.NextRunAt)
		if err != nil || !fired {
			t.Fatalf("fire after %s = %v, %v", status, fired, err)
		}
	}
}
