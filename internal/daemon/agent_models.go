package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/August-H/pearl-cli/openrouter_request"
)

// AgentModel is a model a signed-in CLI offers. Each CLI reports its own list, so
// Pearl doesn't have to keep one up to date.
type AgentModel struct {
	ID   string
	Name string
}

const agentModelsTimeout = 20 * time.Second

// ClaudeSignedIn reports whether the installed Claude Code CLI is signed in.
func ClaudeSignedIn(ctx context.Context) bool {
	return claudeSignIn(ctx).LoggedIn
}

type claudeAccount struct {
	LoggedIn bool   `json:"loggedIn"`
	Email    string `json:"email"`
}

func claudeSignIn(ctx context.Context) claudeAccount {
	path, err := FindClaude()
	if err != nil {
		return claudeAccount{}
	}
	ctx, cancel := context.WithTimeout(ctx, agentModelsTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, "auth", "status")
	command.Env = ClaudeEnvironment()
	output, _ := command.Output()
	var status claudeAccount
	if json.Unmarshal(output, &status) != nil {
		return claudeAccount{}
	}
	return status
}

// ClaudeModels asks the installed CLI which models this account can use. This is
// the CLI's startup request, so it sends no prompt and uses no tokens.
func ClaudeModels(ctx context.Context) ([]AgentModel, error) {
	response, err := claudeControlRequest(ctx, "pearl-models", "initialize")
	if err != nil {
		return nil, err
	}
	var result struct {
		Models []struct {
			Value       string `json:"value"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		return nil, fmt.Errorf("read Claude Code's models: %w", err)
	}
	var models []AgentModel
	for _, model := range result.Models {
		// "default" is an alias for a model that's also listed.
		if model.Value != "" && model.Value != "default" {
			models = append(models, AgentModel{ID: model.Value, Name: model.DisplayName})
		}
	}
	return models, nil
}

// claudeControlRequest starts the installed CLI, sends it one control request and
// returns the answer. It sends no prompt, so it uses no tokens.
func claudeControlRequest(ctx context.Context, requestID, subtype string) (json.RawMessage, error) {
	path, err := FindClaude()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, agentModelsTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, "-p", "--output-format", "stream-json", "--verbose",
		"--input-format", "stream-json", "--no-session-persistence")
	command.Dir = os.TempDir()
	command.Env = ClaudeEnvironment()
	command.WaitDelay = 2 * time.Second
	var stderr codexTailBuffer
	command.Stderr = &stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("connect to Claude Code: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("connect to Claude Code: %w", err)
	}
	stop, err := openrouter_request.StartProcessGroup(command)
	if err != nil {
		return nil, fmt.Errorf("start Claude Code: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		stop()
		_ = command.Wait()
	}()
	request, _ := json.Marshal(map[string]any{
		"type": "control_request", "request_id": requestID, "request": map[string]string{"subtype": subtype},
	})
	if _, err := stdin.Write(append(request, '\n')); err != nil {
		return nil, fmt.Errorf("send Claude Code a request: %w", err)
	}
	reader := bufio.NewReaderSize(stdout, 256<<10)
	for {
		line, readErr := reader.ReadBytes('\n')
		var message struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string          `json:"request_id"`
				Subtype   string          `json:"subtype"`
				Error     string          `json:"error"`
				Response  json.RawMessage `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(line, &message) == nil && message.Type == "control_response" && message.Response.RequestID == requestID {
			if message.Response.Subtype != "success" {
				return nil, errors.New("Claude Code refused the request: " + message.Response.Error)
			}
			return message.Response.Response, nil
		}
		if readErr != nil {
			// Wait first: until then the process's stderr is still being copied.
			_ = stdin.Close()
			_ = command.Wait()
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = readErr.Error()
			}
			return nil, errors.New("Claude Code didn't answer: " + detail)
		}
	}
}

// AntigravityModels starts a session on the installed server and reads the
// choices it offers for this account. It sends no prompt.
func AntigravityModels(ctx context.Context) ([]AgentModel, error) {
	install, err := FindAntigravity()
	if err != nil {
		return nil, err
	}
	if !AntigravitySignedIn() {
		return nil, ErrAntigravitySignIn
	}
	profile, err := prepareAntigravityProfile()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, agentModelsTimeout)
	defer cancel()
	directory := filepath.Join(profile, "tmp")
	command := exec.CommandContext(ctx, install.Server, antigravityServerArguments()...)
	command.Dir = directory
	command.Env = antigravityEnvironment(install, profile, false)
	var signedOut sync.Once
	signIn := make(chan struct{})
	client, stop, err := startAntigravity(command, nil, func(line string) {
		if strings.HasPrefix(line, antigravityAuthLinkPrefix) {
			signedOut.Do(func() {
				close(signIn)
				cancel()
			})
		}
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		stop()
		_ = client.wait()
	}()
	var setup acpSessionSetup
	err = client.initialize(ctx)
	if err == nil {
		err = client.call(ctx, "authenticate", map[string]any{"methodId": antigravityAuthMethod}, nil)
	}
	if err == nil {
		err = client.call(ctx, "session/new", map[string]any{"cwd": directory, "mcpServers": []any{}}, &setup)
	}
	select {
	case <-signIn:
		return nil, ErrAntigravitySignIn
	default:
	}
	if err != nil {
		return nil, fmt.Errorf("ask Antigravity for its models: %w", err)
	}
	for _, option := range setup.ConfigOptions {
		if option.ID == "model" {
			return flattenAntigravityModels(option.Options), nil
		}
	}
	return nil, errors.New("Antigravity didn't list its models")
}

func flattenAntigravityModels(values []acpConfigValue) []AgentModel {
	var models []AgentModel
	for _, value := range values {
		if value.Value != "" {
			models = append(models, AgentModel{ID: value.Value, Name: value.Name})
		}
		models = append(models, flattenAntigravityModels(value.Options)...)
	}
	return models
}
