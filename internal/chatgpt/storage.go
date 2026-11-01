// Package chatgpt implements OpenAI's public-client ChatGPT plan sign-in flow.
// Credentials belong to Pearl and are never imported from other applications.
package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/August-H/pearl-cli/internal/filelock"
	"os"
	"path/filepath"
	"time"

	"github.com/August-H/pearl-cli/internal/pearlpaths"
)

const UsageURL = "https://chatgpt.com/#settings/Usage"

type Account struct {
	ClientID     string    `json:"client_id"`
	Issuer       string    `json:"issuer"`
	Subject      string    `json:"subject"`
	Email        string    `json:"email"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	Scopes       string    `json:"scopes,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type credentials struct {
	HostID   string    `json:"ext_agent_host_id"`
	Active   string    `json:"active_client_id,omitempty"`
	Accounts []Account `json:"accounts,omitempty"`
}

func (s *credentials) active() *Account {
	for i := range s.Accounts {
		if s.Accounts[i].ClientID == s.Active {
			return &s.Accounts[i]
		}
	}
	return nil
}

// withCredentials serializes rotation across the CLI and daemon processes.
func (c *Client) withCredentials(ctx context.Context, fn func(*credentials) error) error {
	paths, err := pearlpaths.Resolve()
	if err != nil {
		return err
	}
	if err := pearlpaths.Ensure(paths); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(paths.Directory, "chatgpt.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		locked, err := filelock.TryLock(lock)
		if err != nil {
			return err
		}
		if locked {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer filelock.Unlock(lock)
	path := filepath.Join(paths.Directory, "chatgpt.json")
	var saved credentials
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &saved); err != nil {
			return fmt.Errorf("read ChatGPT credentials: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fn(&saved)
}

func saveCredentials(saved *credentials) error {
	paths, err := pearlpaths.Resolve()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(paths.Directory, ".chatgpt-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(paths.Directory, "chatgpt.json"))
}

// AccountInfo deliberately excludes all credentials.
type AccountInfo struct {
	ClientID  string
	Email     string
	Active    bool
	Connected bool
	PlanUsage bool
}

func (c *Client) Accounts(ctx context.Context) ([]AccountInfo, error) {
	var accounts []AccountInfo
	err := c.withCredentials(ctx, func(s *credentials) error {
		for _, a := range s.Accounts {
			accounts = append(accounts, AccountInfo{a.ClientID, a.Email, a.ClientID == s.Active, a.AccessToken != "", hasScope(a.Scopes, "chatgpt.tokens.use.direct")})
		}
		return nil
	})
	return accounts, err
}

func (c *Client) Select(ctx context.Context, id string) error {
	return c.withCredentials(ctx, func(s *credentials) error {
		for _, a := range s.Accounts {
			if a.ClientID == id && a.AccessToken != "" {
				s.Active = id
				return saveCredentials(s)
			}
		}
		return errors.New("connected ChatGPT account not found; run `pearl auth accounts`")
	})
}
