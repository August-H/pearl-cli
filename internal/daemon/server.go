package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/pearlpaths"
	"github.com/August-H/pearl-cli/internal/store"
)

type Server struct {
	store   *store.Store
	manager *Manager
	cancel  context.CancelFunc
	started time.Time
}

type submitRequest struct {
	Name          string `json:"name"`
	Prompt        string `json:"prompt"`
	WorkspaceRoot string `json:"workspace_root"`
}

type scheduleRequest struct {
	Name            string `json:"name"`
	Prompt          string `json:"prompt"`
	WorkspaceRoot   string `json:"workspace_root"`
	IntervalSeconds int64  `json:"interval_seconds"`
}

type respondRequest struct {
	Response string `json:"response"`
}

type jobActionResponse struct {
	store.Job
	EventSequence int64 `json:"event_sequence"`
}

type jobDetailsResponse struct {
	Job            store.Job             `json:"job"`
	Transcript     []byte                `json:"transcript,omitempty"`
	ToolExecutions []store.ToolExecution `json:"tool_executions"`
}

func Run(ctx context.Context, paths pearlpaths.Paths, runner AgentRunner) error {
	if err := pearlpaths.Ensure(paths); err != nil {
		return fmt.Errorf("create Pearl runtime directory: %w", err)
	}
	listener, err := listenUnix(paths.Socket)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(paths.Socket)
	}()

	state, err := store.Open(paths.Database)
	if err != nil {
		return fmt.Errorf("open Pearl database: %w", err)
	}
	defer state.Close()
	if _, err := state.RecoverRunningJobs(ctx); err != nil {
		return fmt.Errorf("recover interrupted jobs: %w", err)
	}

	runtimeContext, cancel := context.WithCancel(ctx)
	defer cancel()
	manager := NewManager(state, runner)
	manager.Start(runtimeContext)
	server := &Server{store: state, manager: manager, cancel: cancel, started: time.Now().UTC()}
	httpServer := &http.Server{
		Handler:           server.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveResult := make(chan error, 1)
	go func() { serveResult <- httpServer.Serve(listener) }()

	var serveErr error
	select {
	case <-runtimeContext.Done():
	case serveErr = <-serveResult:
		cancel()
	}
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = httpServer.Shutdown(shutdownContext)
	shutdownCancel()
	manager.Wait()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func listenUnix(socketPath string) (net.Listener, error) {
	if connection, err := net.DialTimeout("unix", socketPath, 250*time.Millisecond); err == nil {
		_ = connection.Close()
		return nil, fmt.Errorf("Pearl daemon is already running at %s", socketPath)
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket path %s", socketPath)
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, fmt.Errorf("remove stale Pearl socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on Pearl socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/jobs", s.handleJobs)
	mux.HandleFunc("/v1/jobs/", s.handleJob)
	mux.HandleFunc("/v1/archive", s.handleArchive)
	mux.HandleFunc("/v1/schedules", s.handleSchedules)
	mux.HandleFunc("/v1/schedules/", s.handleSchedule)
	mux.HandleFunc("/v1/shutdown", s.handleShutdown)
	return mux
}

func (s *Server) handleArchive(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	jobs, err := s.store.ListArchivedJobs(request.Context(), limit)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, jobs)
}

func (s *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	queued, err := s.store.CountJobs(request.Context(), store.JobQueued)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	waitingInput, err := s.store.CountJobs(request.Context(), store.JobWaitingInput)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"running":            true,
		"worker_count":       1,
		"current_job_id":     s.manager.Current(),
		"queued_jobs":        queued,
		"waiting_input_jobs": waitingInput,
		"started_at":         s.started,
	})
}

func (s *Server) handleJobs(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodPost:
		var input submitRequest
		if err := decodeJSON(request, &input); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		input.Prompt = strings.TrimSpace(input.Prompt)
		input.Name = strings.TrimSpace(input.Name)
		if input.Prompt == "" {
			writeError(writer, http.StatusBadRequest, errors.New("prompt cannot be empty"))
			return
		}
		if err := store.ValidateJobName(input.Name); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		workspace, err := validateWorkspace(input.WorkspaceRoot)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		job, err := s.store.CreatePendingNamedJob(
			request.Context(), input.Name, input.Prompt, workspace,
		)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSON(writer, http.StatusAccepted, job)
	case http.MethodGet:
		var jobs []store.Job
		var err error
		if request.URL.Query().Get("active") == "1" {
			jobs, err = s.store.ListActiveJobs(request.Context())
		} else {
			limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
			jobs, err = s.store.ListJobs(request.Context(), limit)
		}
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, jobs)
	default:
		methodNotAllowed(writer)
	}
}

func (s *Server) handleJob(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/v1/jobs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(writer, request)
		return
	}
	jobID := parts[0]
	if len(parts) == 1 {
		switch request.Method {
		case http.MethodGet:
			job, err := s.store.GetJob(request.Context(), jobID)
			if err != nil {
				writeError(writer, http.StatusNotFound, err)
				return
			}
			writeJSON(writer, http.StatusOK, job)
		case http.MethodDelete:
			if err := s.store.ArchiveJob(request.Context(), jobID); err != nil {
				writeError(writer, http.StatusBadRequest, err)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
		default:
			methodNotAllowed(writer)
		}
		return
	}
	if len(parts) != 2 {
		http.NotFound(writer, request)
		return
	}
	switch parts[1] {
	case "details":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		job, err := s.store.GetJob(request.Context(), jobID)
		if err != nil {
			writeError(writer, http.StatusNotFound, err)
			return
		}
		transcript, err := s.store.LoadTranscript(request.Context(), jobID)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		toolExecutions, err := s.store.ListToolExecutions(request.Context(), jobID)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, jobDetailsResponse{
			Job:            job,
			Transcript:     transcript,
			ToolExecutions: toolExecutions,
		})
	case "run":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		job, eventSequence, err := s.store.RunJob(request.Context(), jobID)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		s.manager.Wake()
		writeJSON(writer, http.StatusOK, jobActionResponse{
			Job:           job,
			EventSequence: eventSequence,
		})
	case "cancel":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		job, err := s.store.RequestCancel(request.Context(), jobID)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		s.manager.Cancel(jobID)
		writeJSON(writer, http.StatusOK, job)
	case "retry":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		job, eventSequence, err := s.store.RetryJob(request.Context(), jobID)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		s.manager.Wake()
		writeJSON(writer, http.StatusOK, jobActionResponse{
			Job:           job,
			EventSequence: eventSequence,
		})
	case "respond":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer)
			return
		}
		var input respondRequest
		if err := decodeJSON(request, &input); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		job, eventSequence, err := s.store.RespondToJob(
			request.Context(), jobID, input.Response,
		)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		s.manager.Wake()
		writeJSON(writer, http.StatusOK, jobActionResponse{
			Job:           job,
			EventSequence: eventSequence,
		})
	case "events":
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		s.streamEvents(writer, request, jobID)
	default:
		http.NotFound(writer, request)
	}
}

func (s *Server) streamEvents(writer http.ResponseWriter, request *http.Request, jobID string) {
	if _, err := s.store.GetJob(request.Context(), jobID); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, errors.New("streaming is unavailable"))
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()
	sequence, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := s.store.EventsAfter(request.Context(), jobID, sequence, 200)
		if err != nil {
			return
		}
		for _, event := range events {
			payload, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(writer, "data: %s\n\n", payload); err != nil {
				return
			}
			sequence = event.Sequence
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		job, err := s.store.GetJob(request.Context(), jobID)
		if err != nil || ((job.Terminal() || job.Status == store.JobWaitingInput) && len(events) == 0) {
			return
		}
		select {
		case <-request.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) handleSchedules(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		schedules, err := s.store.ListSchedules(request.Context())
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		writeJSON(writer, http.StatusOK, schedules)
	case http.MethodPost:
		var input scheduleRequest
		if err := decodeJSON(request, &input); err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		workspace, err := validateWorkspace(input.WorkspaceRoot)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(input.Prompt) == "" {
			writeError(writer, http.StatusBadRequest, errors.New("prompt cannot be empty"))
			return
		}
		name := strings.TrimSpace(input.Name)
		if name == "" {
			name = "scheduled task"
		}
		schedule, err := s.store.CreateSchedule(
			request.Context(), name, input.Prompt, workspace,
			time.Duration(input.IntervalSeconds)*time.Second,
		)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err)
			return
		}
		writeJSON(writer, http.StatusCreated, schedule)
	default:
		methodNotAllowed(writer)
	}
}

func (s *Server) handleSchedule(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodDelete {
		methodNotAllowed(writer)
		return
	}
	id := strings.Trim(strings.TrimPrefix(request.URL.Path, "/v1/schedules/"), "/")
	if id == "" {
		http.NotFound(writer, request)
		return
	}
	if err := s.store.DeleteSchedule(request.Context(), id); err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleShutdown(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "stopping"})
	go s.cancel()
}

func validateWorkspace(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("workspace_root cannot be empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace_root %q is not a directory", absolute)
	}
	return absolute, nil
}

func decodeJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 2<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

func methodNotAllowed(writer http.ResponseWriter) {
	writeError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}
