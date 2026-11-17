package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspaceLockCoversSessionsNestedPathsAndAliases(t *testing.T) {
	for _, kind := range []string{"same", "child", "parent", "symlink", "case"} {
		t.Run(kind, func(t *testing.T) {
			state := openTestStore(t)
			ctx := context.Background()
			root := t.TempDir()
			child := filepath.Join(root, "Child")
			if err := os.Mkdir(child, 0o755); err != nil {
				t.Fatal(err)
			}
			firstRoot, nextRoot := root, root
			switch kind {
			case "child":
				nextRoot = child
			case "parent":
				firstRoot = child
			case "symlink":
				nextRoot = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(root, nextRoot); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "case":
				if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
					t.Skip("case sensitive path comparison")
				}
				firstRoot, nextRoot = child, filepath.Join(root, "child")
			}
			first, err := state.CreateJob(ctx, "ordinary", firstRoot)
			if err != nil {
				t.Fatal(err)
			}
			claimed, found, err := state.ClaimNextJob(ctx)
			if err != nil || !found || claimed.ID != first.ID {
				t.Fatalf("first claim = %+v, %v, %v", claimed, found, err)
			}
			for range 2 {
				session, err := state.CreateAutonomousSession(ctx, AutonomousConfig{Goal: "g", WorkspaceRoot: nextRoot, MaxSubagents: 3})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := state.CreateAutonomousJob(ctx, session.ID, "work", "", ""); err != nil {
					t.Fatal(err)
				}
			}
			if got := claimAll(t, state); len(got) != 0 {
				t.Fatalf("overlapping workspaces claimed: %v", got)
			}
			// A blocked candidate must not prevent an independent session running.
			separate, err := state.CreateAutonomousSession(ctx, AutonomousConfig{Goal: "g", WorkspaceRoot: t.TempDir(), MaxSubagents: 1})
			if err != nil {
				t.Fatal(err)
			}
			independent, err := state.CreateAutonomousJob(ctx, separate.ID, "independent", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if got := claimAll(t, state); len(got) != 1 || got[0] != independent.ID {
				t.Fatalf("independent claim = %v", got)
			}
			if err := state.FinishJob(ctx, first.ID, JobCompleted, "ok", ""); err != nil {
				t.Fatal(err)
			}
			if got := claimAll(t, state); len(got) != 1 {
				t.Fatalf("released workspace claims = %v, want one", got)
			}
		})
	}
}

func TestWorkspaceLockReleasesAfterRecovery(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	workspace := t.TempDir()
	for range 2 {
		session, err := state.CreateAutonomousSession(ctx, AutonomousConfig{Goal: "g", WorkspaceRoot: workspace, MaxSubagents: 2})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := state.CreateAutonomousJob(ctx, session.ID, "work", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := claimAll(t, state); len(got) != 1 {
		t.Fatalf("claims = %v", got)
	}
	if _, err := state.RecoverRunningJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if got := claimAll(t, state); len(got) != 1 {
		t.Fatalf("after recovery claims = %v", got)
	}
}
