package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/August-H/pearl-cli/openrouter_request"
)

var ErrSubscriptionInvalid = errors.New("subscription sign-in needs refreshing")

// CheckClaudeSubscription asks the installed CLI, without a prompt. The bool
// reports whether it found an account, so unused providers can stay quiet.
func CheckClaudeSubscription(ctx context.Context) (bool, error) {
	if BillingFor(openrouter_request.ProviderClaude) == BillingAPI {
		return false, ErrNoPlanLimits
	}
	account, err := claudeSignInStatus(ctx)
	if err != nil {
		return account.LoggedIn, err
	}
	if !account.LoggedIn {
		return false, ErrClaudeSignedOut
	}
	_, err = claudeControlRequest(ctx, "pearl-subscription", "get_usage")
	if err != nil {
		message := strings.ToLower(err.Error())
		for _, marker := range []string{"authentication_error", "unauthorized", "not logged in", "please log in", "login required", "token expired", "token has expired", "invalid_grant", "http 401", "http 403", "status code 401", "status code 403"} {
			if strings.Contains(message, marker) {
				return true, ErrSubscriptionInvalid
			}
		}
	}
	return true, err
}

// CheckAntigravitySubscription validates the saved Google refresh token directly.
// Starting the ACP bundle for a check would unpack a large agent installation.
func CheckAntigravitySubscription(ctx context.Context) (bool, error) {
	if !AntigravitySignedIn() {
		return false, ErrAntigravitySignIn
	}
	_, _, err := antigravityAccessToken(ctx)
	return true, err
}

func antigravityAccessToken(ctx context.Context) (string, string, error) {
	profile, err := AntigravityProfile()
	if err != nil {
		return "", "", err
	}
	contents, err := os.ReadFile(filepath.Join(profile, "antigravity-acp", antigravityTokenFile))
	if err != nil {
		return "", "", err
	}
	var saved struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		RefreshToken string `json:"refresh_token"`
		ProjectID    string `json:"project_id"`
	}
	if json.Unmarshal(contents, &saved) != nil || saved.ClientID == "" || saved.RefreshToken == "" {
		return "", "", fmt.Errorf("%w: the saved Google sign-in can't be read", ErrSubscriptionInvalid)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravityTokenEndpoint, strings.NewReader(url.Values{
		"client_id": {saved.ClientID}, "client_secret": {saved.ClientSecret},
		"refresh_token": {saved.RefreshToken}, "grant_type": {"refresh_token"},
	}.Encode()))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := antigravityClient.Do(request)
	if err != nil {
		return "", "", errors.New("could not reach Google sign-in")
	}
	defer response.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, usageResponseBytes)).Decode(&token)
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden ||
		(response.StatusCode == http.StatusBadRequest && (token.Error == "invalid_grant" || token.Error == "invalid_token" || token.Error == "invalid_client")) {
		return "", "", fmt.Errorf("%w: Google rejected the saved sign-in", ErrSubscriptionInvalid)
	}
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("Google sign-in check returned HTTP %d", response.StatusCode)
	}
	if decodeErr != nil || token.AccessToken == "" {
		return "", "", errors.New("Google returned an incomplete sign-in response")
	}
	return token.AccessToken, saved.ProjectID, nil
}
