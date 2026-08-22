package store

import "time"

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

type Job struct {
	ID              string     `json:"id"`
	Prompt          string     `json:"prompt"`
	WorkspaceRoot   string     `json:"workspace_root"`
	Status          string     `json:"status"`
	Result          string     `json:"result,omitempty"`
	Error           string     `json:"error,omitempty"`
	CancelRequested bool       `json:"cancel_requested"`
	Question        string     `json:"question,omitempty"`
	InputToolCallID string     `json:"-"`
	InputResponse   string     `json:"-"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	ArchivedAt      *time.Time `json:"archived_at,omitempty"`
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

type AutonomousSession struct {
	ID            string     `json:"id"`
	Goal          string     `json:"goal"`
	WorkspaceRoot string     `json:"workspace_root"`
	Status        string     `json:"status"`
	Summary       string     `json:"summary,omitempty"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

func (session AutonomousSession) Terminal() bool {
	switch session.Status {
	case AutonomousCompleted, AutonomousFailed, AutonomousCancelled:
		return true
	default:
		return false
	}
}

type AutonomousDetails struct {
	Session AutonomousSession `json:"session"`
	Jobs    []Job             `json:"jobs"`
	Events  []Event           `json:"events,omitempty"`
}
