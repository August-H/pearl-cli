package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	issuer          = "https://auth.openai.com"
	resource        = "https://api.openai.com/v1"
	dynamicClient   = "dynamic_agent_client"
	requestedScopes = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
)

type Client struct {
	// Private endpoint overrides allow tests without real credentials or usage.
	issuerURL string
	apiURL    string
	http      *http.Client
	accountID string
}

// Pin keeps a running job on its original account when the user switches accounts.
func (c *Client) Pin(ctx context.Context) (*Client, error) {
	copyClient := *c
	err := c.withCredentials(ctx, func(s *credentials) error {
		if s.active() == nil || s.active().AccessToken == "" {
			return errors.New("ChatGPT is not connected; run `pearl auth login chatgpt`")
		}
		copyClient.accountID = s.Active
		return nil
	})
	return &copyClient, err
}

func NewClient() *Client { return &Client{} }

// RegistrationID identifies a pinned account without exposing credentials.
func (c *Client) RegistrationID() string { return c.accountID }
func (c *Client) authURL() string {
	if c.issuerURL != "" {
		return c.issuerURL
	}
	return issuer
}
func (c *Client) BaseURL() string {
	if c.apiURL != "" {
		return c.apiURL
	}
	return resource
}
func (c *Client) httpClient() *http.Client {
	if c.http != nil {
		return c.http
	}
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func randomValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hasScope(scopes, wanted string) bool {
	for _, scope := range strings.Fields(scopes) {
		if scope == wanted {
			return true
		}
	}
	return false
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

func (c *Client) exchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	var token tokenResponse
	req, err := http.NewRequestWithContext(ctx, "POST", c.authURL()+"/api/accounts/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return token, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return token, errors.New("could not reach ChatGPT sign-in; try again")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&failure)
		switch failure.Error {
		case "invalid_grant", "invalid_token":
			return token, errors.New("ChatGPT session expired or was revoked; run `pearl auth login chatgpt` again")
		default:
			return token, fmt.Errorf("ChatGPT token exchange failed (HTTP %d); try signing in again", resp.StatusCode)
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return token, errors.New("invalid ChatGPT token response")
	}
	if token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") || token.ExpiresIn <= 0 {
		return token, errors.New("incomplete ChatGPT token response")
	}
	return token, nil
}

// Login opens a loopback callback before passing the authorization URL to open.
// accountID is an existing registration to reauthorize, or empty for a new one.
func (c *Client) Login(ctx context.Context, accountID string, open func(string) error) error {
	var previous Account
	var hostID string
	err := c.withCredentials(ctx, func(s *credentials) error {
		if s.HostID == "" {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			b[6] = (b[6] & 0x0f) | 0x40
			b[8] = (b[8] & 0x3f) | 0x80
			s.HostID = fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
		}
		hostID = s.HostID
		if accountID != "" {
			for _, a := range s.Accounts {
				if a.ClientID == accountID {
					previous = a
					break
				}
			}
			if previous.ClientID == "" {
				return errors.New("ChatGPT account not found")
			}
		}
		return saveCredentials(s)
	})
	if err != nil {
		return err
	}
	state, err := randomValue()
	if err != nil {
		return err
	}
	nonce, err := randomValue()
	if err != nil {
		return err
	}
	verifier, err := randomValue()
	if err != nil {
		return err
	}
	challenge := sha256.Sum256([]byte(verifier))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start ChatGPT callback: %w", err)
	}
	redirect := "http://" + listener.Addr().String() + "/auth/callback"
	type callback struct {
		code, clientID string
		err            error
	}
	result := make(chan callback, 1)
	var once sync.Once
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != "GET" || r.URL.Path != "/auth/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Sign-in state did not match. Return to Pearl and retry.", http.StatusBadRequest)
			return
		}
		value := callback{code: q.Get("code"), clientID: q.Get("client_id")}
		switch {
		case q.Get("error") != "":
			value.err = errors.New("ChatGPT sign-in was declined or failed; no account was changed")
		case value.code == "":
			value.err = errors.New("ChatGPT callback did not include an authorization code")
		case previous.ClientID == "" && (value.clientID == "" || value.clientID == dynamicClient):
			value.err = errors.New("ChatGPT registration did not return an issued client ID")
		case previous.ClientID != "" && value.clientID != "" && value.clientID != previous.ClientID:
			value.err = errors.New("ChatGPT registration changed during sign-in")
		}
		if previous.ClientID != "" {
			value.clientID = previous.ClientID
		}
		once.Do(func() { result <- value })
		fmt.Fprintln(w, "Return to Pearl to finish signing in. You can close this tab.")
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	q := url.Values{"client_id": {dynamicClient}, "agent_name_hint": {"Pearl"}, "ext_agent_host_id": {hostID}, "response_type": {"code"}, "redirect_uri": {redirect}, "scope": {requestedScopes}, "resource": {resource}, "state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}}
	if previous.ClientID != "" {
		q.Set("client_id", previous.ClientID)
		q.Del("agent_name_hint")
		// Do not put retained ID tokens in a URL printed by a CLI.
		if previous.Email != "" {
			q.Set("login_hint", previous.Email)
		}
	}
	if err := open(c.authURL() + "/api/accounts/authorize?" + q.Encode()); err != nil {
		return err
	}
	var returned callback
	select {
	case <-ctx.Done():
		return fmt.Errorf("ChatGPT sign-in stopped: %w", ctx.Err())
	case returned = <-result:
	}
	if returned.err != nil {
		return returned.err
	}
	// Retain an issued registration even if exchange fails, for a fresh login.
	if previous.ClientID == "" {
		if err := c.withCredentials(ctx, func(s *credentials) error {
			for _, a := range s.Accounts {
				if a.ClientID == returned.clientID {
					return errors.New("ChatGPT returned an existing registration for a new account")
				}
			}
			s.Accounts = append(s.Accounts, Account{ClientID: returned.clientID})
			return saveCredentials(s)
		}); err != nil {
			return err
		}
	}
	token, err := c.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {returned.clientID}, "code": {returned.code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {resource}})
	if err != nil {
		return err
	}
	identity, err := c.verifyIdentity(ctx, token.IDToken, returned.clientID, nonce)
	if err != nil {
		return err
	}
	if previous.Subject != "" && (identity.Subject != previous.Subject || identity.Issuer != previous.Issuer) {
		return errors.New("ChatGPT account changed during sign-in; use --new-account to link another account")
	}
	if !hasScope(token.Scope, "chatgpt.tokens.use.direct") || !hasScope(token.Scope, "resource.invoke") {
		return errors.New("ChatGPT plan usage was not granted; sign in again and allow plan usage")
	}
	if token.RefreshToken == "" {
		return errors.New("ChatGPT did not grant a renewable session; sign in again")
	}
	account := Account{ClientID: returned.clientID, Issuer: identity.Issuer, Subject: identity.Subject, Email: identity.Email, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, IDToken: token.IDToken, Scopes: token.Scope, ExpiresAt: time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)}
	return c.withCredentials(ctx, func(s *credentials) error {
		for i, a := range s.Accounts {
			if a.ClientID == account.ClientID {
				s.Accounts[i] = account
				s.Active = account.ClientID
				return saveCredentials(s)
			}
		}
		return errors.New("ChatGPT registration disappeared during sign-in; retry")
	})
}

// AccessToken reloads credentials for each request and rotates under a file lock.
func (c *Client) AccessToken(ctx context.Context) (string, error) {
	var value string
	err := c.withCredentials(ctx, func(s *credentials) error {
		a := s.active()
		if c.accountID != "" {
			a = nil
			for i := range s.Accounts {
				if s.Accounts[i].ClientID == c.accountID {
					a = &s.Accounts[i]
					break
				}
			}
		}
		if a == nil || a.AccessToken == "" {
			return errors.New("ChatGPT is not connected; run `pearl auth login chatgpt`")
		}
		if !hasScope(a.Scopes, "chatgpt.tokens.use.direct") || !hasScope(a.Scopes, "resource.invoke") {
			return errors.New("ChatGPT plan usage is not enabled; run `pearl auth login chatgpt`")
		}
		if time.Until(a.ExpiresAt) < 30*time.Second {
			if a.RefreshToken == "" {
				return errors.New("ChatGPT session expired; run `pearl auth login chatgpt`")
			}
			token, err := c.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {a.ClientID}, "refresh_token": {a.RefreshToken}, "resource": {resource}})
			if err != nil {
				return err
			}
			if token.RefreshToken == "" {
				return errors.New("ChatGPT refresh response omitted its replacement refresh token; sign in again")
			}
			a.AccessToken = token.AccessToken
			a.RefreshToken = token.RefreshToken
			a.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
			if token.Scope != "" {
				a.Scopes = token.Scope
			}
			if err := saveCredentials(s); err != nil {
				return err
			}
			if !hasScope(a.Scopes, "chatgpt.tokens.use.direct") || !hasScope(a.Scopes, "resource.invoke") {
				return errors.New("ChatGPT plan usage permission was removed; sign in again")
			}
		}
		value = a.AccessToken
		return nil
	})
	return value, err
}

// Logout revokes the active renewable session, then clears local tokens even
// if the network is unavailable. Registration and host identity stay saved.
func (c *Client) Logout(ctx context.Context) error {
	var revokeErr error
	err := c.withCredentials(ctx, func(s *credentials) error {
		a := s.active()
		if a == nil {
			return nil
		}
		if a.RefreshToken != "" {
			revokeErr = c.revoke(ctx, a)
		}
		a.AccessToken = ""
		a.RefreshToken = ""
		a.IDToken = ""
		a.Scopes = ""
		a.ExpiresAt = time.Time{}
		return saveCredentials(s)
	})
	if err != nil {
		return err
	}
	if revokeErr != nil {
		return errors.New("signed out locally; remote revocation was not confirmed. Disconnect Pearl in ChatGPT Settings to revoke access")
	}
	return nil
}

func (c *Client) revoke(ctx context.Context, a *Account) error {
	var failure error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}
		var discovery struct {
			Endpoint string `json:"revocation_endpoint"`
		}
		if err := c.getJSON(ctx, c.authURL()+"/.well-known/openid-configuration", &discovery); err != nil {
			failure = err
			continue
		}
		if !strings.HasPrefix(discovery.Endpoint, c.authURL()+"/") {
			return errors.New("invalid revocation endpoint")
		}
		form := url.Values{"token": {a.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {a.ClientID}}
		req, err := http.NewRequestWithContext(ctx, "POST", discovery.Endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := c.httpClient().Do(req)
		if err != nil {
			failure = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return nil
		}
		failure = errors.New("revocation failed")
		if resp.StatusCode < 500 {
			return failure
		}
	}
	return failure
}

func (c *Client) getJSON(ctx context.Context, endpoint string, value any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return errors.New("could not reach ChatGPT")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("ChatGPT returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(value); err != nil {
		return errors.New("invalid ChatGPT response")
	}
	return nil
}
