package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/August-H/pearl-cli/internal/resource"
	"time"
)

// ResourceLimits applies to all jobs and orchestrator turns in this database.
// Usage caps are lifetime totals, retained across daemon restarts. Zero disables
// a usage cap. Zero concurrency and job limits select the defaults.
type ResourceLimits struct {
	MaxConcurrency    int     `json:"max_concurrency"`
	MaxSessionJobs    int     `json:"max_session_jobs"`
	MaxTokens         int64   `json:"max_tokens"`
	MaxCostUSD        float64 `json:"max_cost_usd"`
	SessionMaxTokens  int64   `json:"session_max_tokens"`
	SessionMaxCostUSD float64 `json:"session_max_cost_usd"`
}

func (l ResourceLimits) normalized() (ResourceLimits, error) {
	if l.MaxConcurrency < 0 || l.MaxSessionJobs < 0 || !(resource.Usage{Tokens: l.MaxTokens, CostUSD: l.MaxCostUSD}).Valid() || !(resource.Usage{Tokens: l.SessionMaxTokens, CostUSD: l.SessionMaxCostUSD}).Valid() {
		return l, errors.New("resource limits must be finite and nonnegative")
	}
	if l.MaxConcurrency == 0 {
		l.MaxConcurrency = 4
	}
	if l.MaxSessionJobs == 0 {
		l.MaxSessionJobs = 100
	}
	return l, nil
}

func (s *Store) SetResourceLimits(ctx context.Context, limits ResourceLimits) error {
	limits, err := limits.normalized()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE resource_limits SET max_concurrency=?, max_session_jobs=?, max_tokens=?, max_cost_usd=?, session_max_tokens=?, session_max_cost_usd=? WHERE id=1`, limits.MaxConcurrency, limits.MaxSessionJobs, limits.MaxTokens, limits.MaxCostUSD, limits.SessionMaxTokens, limits.SessionMaxCostUSD)
	return err
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readResourceLimits(ctx context.Context, q rowQuerier) (ResourceLimits, error) {
	var l ResourceLimits
	err := q.QueryRowContext(ctx, `SELECT max_concurrency, max_session_jobs, max_tokens, max_cost_usd, session_max_tokens, session_max_cost_usd FROM resource_limits WHERE id=1`).Scan(&l.MaxConcurrency, &l.MaxSessionJobs, &l.MaxTokens, &l.MaxCostUSD, &l.SessionMaxTokens, &l.SessionMaxCostUSD)
	return l, err
}

func readUsage(ctx context.Context, q rowQuerier, sessionID string) (resource.Usage, error) {
	var u resource.Usage
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(tokens),0), COALESCE(SUM(cost_usd),0) FROM resource_usage WHERE (? = '' OR session_id = ?)`, sessionID, sessionID).Scan(&u.Tokens, &u.CostUSD)
	return u, err
}

func budgetError(u resource.Usage, tokens int64, cost float64, scope string) error {
	if tokens > 0 && u.Tokens >= tokens {
		return fmt.Errorf("%w: %s token limit %d reached", resource.ErrBudgetExceeded, scope, tokens)
	}
	if cost > 0 && u.CostUSD >= cost {
		return fmt.Errorf("%w: %s spending limit $%.6f reached", resource.ErrBudgetExceeded, scope, cost)
	}
	return nil
}

func checkResourceBudget(ctx context.Context, q rowQuerier, sessionID string) error {
	limits, err := readResourceLimits(ctx, q)
	if err != nil {
		return err
	}
	usage, err := readUsage(ctx, q, "")
	if err != nil {
		return err
	}
	if err := budgetError(usage, limits.MaxTokens, limits.MaxCostUSD, "daemon"); err != nil {
		return err
	}
	if sessionID == "" {
		return nil
	}
	var tokens int64
	var cost float64
	if err := q.QueryRowContext(ctx, `SELECT max_tokens, max_cost_usd FROM autonomous_sessions WHERE id=?`, sessionID).Scan(&tokens, &cost); err != nil {
		return err
	}
	usage, err = readUsage(ctx, q, sessionID)
	if err != nil {
		return err
	}
	return budgetError(usage, tokens, cost, "session")
}

func (s *Store) CheckResourceBudget(ctx context.Context, jobID, sessionID string) error {
	if jobID != "" {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT session_id FROM autonomous_jobs WHERE job_id=?),'')`, jobID).Scan(&sessionID); err != nil {
			return err
		}
	}
	return checkResourceBudget(ctx, s.db, sessionID)
}

// RecordUsage always commits reported usage, even when this report exhausts a
// cap. Retries and resumed jobs add to their original session's totals.
func (s *Store) RecordUsage(ctx context.Context, jobID, sessionID string, usage resource.Usage) error {
	if !usage.Valid() {
		return errors.New("invalid provider usage")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if jobID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT session_id FROM autonomous_jobs WHERE job_id=?),'')`, jobID).Scan(&sessionID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO resource_usage(job_id,session_id,tokens,cost_usd,created_at) VALUES(?,?,?,?,?)`, jobID, sessionID, usage.Tokens, usage.CostUSD, nowText()); err != nil {
		return err
	}
	exceeded := checkResourceBudget(ctx, tx, sessionID)
	if err := tx.Commit(); err != nil {
		return err
	}
	return exceeded
}

type ResourceStatus struct {
	Limits    ResourceLimits `json:"limits"`
	Usage     resource.Usage `json:"usage"`
	Exhausted string         `json:"exhausted,omitempty"`
}

func (s *Store) ResourceStatus(ctx context.Context) (ResourceStatus, error) {
	l, err := readResourceLimits(ctx, s.db)
	if err != nil {
		return ResourceStatus{}, err
	}
	u, err := readUsage(ctx, s.db, "")
	if err != nil {
		return ResourceStatus{}, err
	}
	status := ResourceStatus{Limits: l, Usage: u}
	if err := budgetError(u, l.MaxTokens, l.MaxCostUSD, "daemon"); err != nil {
		status.Exhausted = err.Error()
	}
	return status, nil
}

// BudgetContext binds all provider calls in a run to durable daemon and session totals.
func (s *Store) BudgetContext(ctx context.Context, jobID, sessionID string) context.Context {
	return resource.WithBudget(ctx,
		func(ctx context.Context) error { return s.CheckResourceBudget(ctx, jobID, sessionID) },
		func(ctx context.Context, u resource.Usage) error {
			// A completed provider report must survive a simultaneous cancellation.
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			return s.RecordUsage(writeCtx, jobID, sessionID, u)
		})
}
