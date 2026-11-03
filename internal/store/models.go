package store

import (
	"fmt"
	"strings"
	"time"
)

const (
	JobQueued       = "queued"
	JobRunning      = "running"
	JobWaitingInput = "waiting_input"
	JobCompleted    = "completed"
	JobPending      = "pending"
	JobFailed       = "failed"
	JobCancelled    = "cancelled"
	JobInterrupted  = "interrupted"
)

const (
	HarnessPearl       = "pearl"
	HarnessCodex       = "codex"
	HarnessClaude      = "claude"
	HarnessAntigravity = "antigravity"
)

// NormalizeHarness returns the canonical harness name. An empty name means the
// job uses the default harness from Pearl's settings when it starts.
func NormalizeHarness(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "", HarnessPearl, HarnessCodex, HarnessClaude, HarnessAntigravity:
		return name, nil
	}
	return "", fmt.Errorf("unknown harness %q; use %q, %q, %q or %q", name, HarnessPearl, HarnessCodex, HarnessClaude, HarnessAntigravity)
}

type Job struct {
	ID            string `json:"id"`
	Prompt        string `json:"prompt"`
	WorkspaceRoot string `json:"workspace_root"`
	Harness       string `json:"harness,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	// AssignedProvider and AssignedModel are what the orchestrator picked. Other
	// jobs leave them empty and use the selected model when they start.
	AssignedProvider    string     `json:"assigned_provider,omitempty"`
	AssignedModel       string     `json:"assigned_model,omitempty"`
	Verification        string     `json:"verification,omitempty"`
	VerificationCommand string     `json:"verification_command,omitempty"`
	Folder              string     `json:"folder,omitempty"`
	Status              string     `json:"status"`
	Result              string     `json:"result,omitempty"`
	Error               string     `json:"error,omitempty"`
	CancelRequested     bool       `json:"cancel_requested"`
	Question            string     `json:"question,omitempty"`
	InputToolCallID     string     `json:"-"`
	InputResponse       string     `json:"-"`
	CreatedAt           time.Time  `json:"created_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
	ArchivedAt          *time.Time `json:"archived_at,omitempty"`
}

func (j Job) Terminal() bool {
	switch j.Status {
	case JobCompleted, JobPending, JobFailed, JobCancelled, JobInterrupted:
		return true
	default:
		return false
	}
}

type Event struct {
	Sequence  int64     `json:"sequence"`
	JobID     string    `json:"job_id"`
	Type      string    `json:"type"`
	Data      string    `json:"data"`
	CreatedAt time.Time `json:"created_at"`
}

type ToolExecution struct {
	JobID      string    `json:"job_id"`
	ToolCallID string    `json:"tool_call_id"`
	ToolName   string    `json:"tool_name"`
	Arguments  string    `json:"arguments"`
	Result     string    `json:"result"`
	CreatedAt  time.Time `json:"created_at"`
}

// Folder groups jobs that run in one directory.
type Folder struct {
	Name          string    `json:"name"`
	WorkspaceRoot string    `json:"workspace_root"`
	CreatedAt     time.Time `json:"created_at"`
}

type Schedule struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Prompt          string    `json:"prompt"`
	WorkspaceRoot   string    `json:"workspace_root"`
	IntervalSeconds int64     `json:"interval_seconds"`
	NextRunAt       time.Time `json:"next_run_at"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
}

const (
	AutonomousRunning      = "running"
	AutonomousReviewing    = "reviewing"
	AutonomousWaitingInput = "waiting_input"
	AutonomousCompleted    = "completed"
	AutonomousFailed       = "failed"
	AutonomousCancelled    = "cancelled"
)

// AutonomousModel is a model an orchestrator can use or assign.
type AutonomousModel struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
}

// AutonomousConfig is what the user chooses when starting a session.
type AutonomousConfig struct {
	Goal          string            `json:"goal"`
	WorkspaceRoot string            `json:"workspace_root"`
	Orchestrator  AutonomousModel   `json:"orchestrator"`
	AllowedModels []AutonomousModel `json:"allowed_models"`
	// AllModels records that the user left the list unrestricted.
	AllModels    bool `json:"all_models"`
	MaxSubagents int  `json:"max_subagents"`
}

type AutonomousSession struct {
	ID            string            `json:"id"`
	Goal          string            `json:"goal"`
	WorkspaceRoot string            `json:"workspace_root"`
	Status        string            `json:"status"`
	Summary       string            `json:"summary,omitempty"`
	Error         string            `json:"error,omitempty"`
	Orchestrator  AutonomousModel   `json:"orchestrator"`
	AllowedModels []AutonomousModel `json:"allowed_models,omitempty"`
	AllModels     bool              `json:"all_models,omitempty"`
	MaxSubagents  int               `json:"max_subagents"`
	Plan          string            `json:"plan,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	FinishedAt    *time.Time        `json:"finished_at,omitempty"`
}

func (session AutonomousSession) Terminal() bool {
	switch session.Status {
	case AutonomousCompleted, AutonomousFailed, AutonomousCancelled:
		return true
	default:
		return false
	}
}

// AutonomousTurn is one time the orchestrator was asked what to do next.
type AutonomousTurn struct {
	Turn int `json:"turn"`
	// Trigger says why it was asked, and Outcome what came of it.
	Trigger   string `json:"trigger,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	// DecisionBytes is how much of its decision it has written so far.
	DecisionBytes int       `json:"decision_bytes,omitempty"`
	Outcome       string    `json:"outcome,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	// UpdatedAt is when the model last said something.
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type AutonomousDetails struct {
	Session AutonomousSession `json:"session"`
	Jobs    []Job             `json:"jobs"`
	Events  []Event           `json:"events,omitempty"`
	Turns   []AutonomousTurn  `json:"turns,omitempty"`
	// JobActivity is the latest thing each job did.
	JobActivity map[string]string `json:"job_activity,omitempty"`
}
