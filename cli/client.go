package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/pearlpaths"
	"github.com/August-H/pearl-cli/internal/store"
)

type daemonClient struct {
	http *http.Client
}

type jobDetails struct {
	Job            store.Job             `json:"job"`
	Transcript     []byte                `json:"transcript,omitempty"`
	ToolExecutions []store.ToolExecution `json:"tool_executions"`
	StatusEvents   []store.Event         `json:"status_events"`
}

func newDaemonClient() (*daemonClient, error) {
	paths, err := pearlpaths.Resolve()
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", paths.Socket)
		},
	}
	return &daemonClient{http: &http.Client{Transport: transport}}, nil
}

func (c *daemonClient) request(
	ctx context.Context,
	method, path string,
	input, output any,
) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://pearl"+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Pearl daemon is unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var daemonError struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&daemonError); err == nil && daemonError.Error != "" {
			return errors.New(daemonError.Error)
		}
		return fmt.Errorf("Pearl daemon returned %s", response.Status)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func (c *daemonClient) submit(ctx context.Context, prompt, workspace string) (store.Job, error) {
	return c.submitNamed(ctx, "", prompt, workspace)
}

func (c *daemonClient) submitNamed(
	ctx context.Context,
	name, prompt, workspace string,
) (store.Job, error) {
	var job store.Job
	input := map[string]string{"prompt": prompt, "workspace_root": workspace}
	if name = strings.TrimSpace(name); name != "" {
		input["name"] = name
	}
	err := c.request(ctx, http.MethodPost, "/v1/jobs", input, &job)
	return job, err
}

func (c *daemonClient) jobs(ctx context.Context) ([]store.Job, error) {
	var jobs []store.Job
	err := c.request(ctx, http.MethodGet, "/v1/jobs?limit=500", nil, &jobs)
	return jobs, err
}

func (c *daemonClient) archivedJobs(ctx context.Context) ([]store.Job, error) {
	var jobs []store.Job
	err := c.request(ctx, http.MethodGet, "/v1/archive", nil, &jobs)
	return jobs, err
}

func (c *daemonClient) createAutonomousSession(
	ctx context.Context,
	goal, workspace string,
) (store.AutonomousSession, error) {
	var session store.AutonomousSession
	err := c.request(ctx, http.MethodPost, "/v1/autonomous", map[string]string{
		"goal": goal, "workspace_root": workspace,
	}, &session)
	return session, err
}

func (c *daemonClient) latestAutonomousSession(
	ctx context.Context,
) (store.AutonomousSession, error) {
	var session store.AutonomousSession
	err := c.request(ctx, http.MethodGet, "/v1/autonomous", nil, &session)
	return session, err
}

func (c *daemonClient) autonomousDetails(
	ctx context.Context,
	id string,
) (store.AutonomousDetails, error) {
	var details store.AutonomousDetails
	err := c.request(
		ctx, http.MethodGet, "/v1/autonomous/"+url.PathEscape(id), nil, &details,
	)
	return details, err
}

func (c *daemonClient) job(ctx context.Context, id string) (store.Job, error) {
	var job store.Job
	err := c.request(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(id), nil, &job)
	return job, err
}

func (c *daemonClient) archiveJob(ctx context.Context, id string) error {
	return c.request(
		ctx, http.MethodDelete, "/v1/jobs/"+url.PathEscape(id), nil, nil,
	)
}

func (c *daemonClient) jobDetails(ctx context.Context, id string) (jobDetails, error) {
	var details jobDetails
	err := c.request(
		ctx,
		http.MethodGet,
		"/v1/jobs/"+url.PathEscape(id)+"/details",
		nil,
		&details,
	)
	return details, err
}

func (c *daemonClient) jobAction(ctx context.Context, id, action string) (store.Job, error) {
	var job store.Job
	err := c.request(ctx, http.MethodPost,
		"/v1/jobs/"+url.PathEscape(id)+"/"+action, map[string]string{}, &job)
	return job, err
}

func (c *daemonClient) retryJob(ctx context.Context, id string) (store.Job, int64, error) {
	var response struct {
		store.Job
		EventSequence int64 `json:"event_sequence"`
	}
	err := c.request(ctx, http.MethodPost,
		"/v1/jobs/"+url.PathEscape(id)+"/retry", map[string]string{}, &response)
	return response.Job, response.EventSequence, err
}

func (c *daemonClient) startJob(ctx context.Context, id string) (store.Job, int64, error) {
	var response struct {
		store.Job
		EventSequence int64 `json:"event_sequence"`
	}
	err := c.request(ctx, http.MethodPost,
		"/v1/jobs/"+url.PathEscape(id)+"/run", map[string]string{}, &response)
	return response.Job, response.EventSequence, err
}

func (c *daemonClient) respondToJob(
	ctx context.Context,
	id, responseText string,
) (store.Job, int64, error) {
	var response struct {
		store.Job
		EventSequence int64 `json:"event_sequence"`
	}
	err := c.request(ctx, http.MethodPost,
		"/v1/jobs/"+url.PathEscape(id)+"/respond",
		map[string]string{"response": responseText}, &response)
	return response.Job, response.EventSequence, err
}

func (c *daemonClient) status(ctx context.Context) (map[string]any, error) {
	var status map[string]any
	err := c.request(ctx, http.MethodGet, "/v1/status", nil, &status)
	return status, err
}

func (c *daemonClient) shutdown(ctx context.Context) error {
	return c.request(ctx, http.MethodPost, "/v1/shutdown", map[string]string{}, nil)
}

func (c *daemonClient) streamEvents(
	ctx context.Context,
	jobID string,
	after int64,
	consume func(store.Event) error,
) error {
	endpoint := "http://pearl/v1/jobs/" + url.PathEscape(jobID) +
		"/events?after=" + strconv.FormatInt(after, 10)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Pearl daemon is unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Pearl daemon returned %s", response.Status)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		var event store.Event
		if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &event); err != nil {
			return err
		}
		if err := consume(event); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (c *daemonClient) schedules(ctx context.Context) ([]store.Schedule, error) {
	var schedules []store.Schedule
	err := c.request(ctx, http.MethodGet, "/v1/schedules", nil, &schedules)
	return schedules, err
}

func (c *daemonClient) createSchedule(
	ctx context.Context,
	name, prompt, workspace string,
	interval time.Duration,
) (store.Schedule, error) {
	var schedule store.Schedule
	err := c.request(ctx, http.MethodPost, "/v1/schedules", map[string]any{
		"name": name, "prompt": prompt, "workspace_root": workspace,
		"interval_seconds": int64(interval / time.Second),
	}, &schedule)
	return schedule, err
}

func (c *daemonClient) deleteSchedule(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete,
		"/v1/schedules/"+url.PathEscape(id), nil, nil)
}
