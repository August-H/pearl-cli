package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckConnectionUsesRequestedAccountWithoutSelectingIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected inference request: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer rejected" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"models": []Model{{ID: "test-model", Visibility: "list"}}})
	}))
	defer server.Close()
	client := &Client{apiURL: server.URL}
	testCredentials(t, client, time.Now().Add(time.Hour))
	err := client.withCredentials(context.Background(), func(s *credentials) error {
		s.Accounts = append(s.Accounts, Account{ClientID: "inactive", AccessToken: "rejected", Scopes: requestedScopes, ExpiresAt: time.Now().Add(time.Hour)})
		return saveCredentials(s)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckConnection(context.Background(), "inactive"); !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("inactive connection = %v", err)
	}
	if err := client.CheckConnection(context.Background(), "oaiapp_test"); err != nil {
		t.Fatal(err)
	}
	if active := readCredentials(t, client).Active; active != "oaiapp_test" {
		t.Fatalf("selected account changed to %s", active)
	}
}

func TestConnectionRefreshDistinguishesRejectionFromTemporaryFailure(t *testing.T) {
	for _, test := range []struct {
		status  int
		code    string
		invalid bool
	}{
		{400, "invalid_grant", true}, {400, "refresh_token_reused", true},
		{400, "refresh_token_expired", true}, {400, "invalid_refresh_token", true},
		{503, "server_error", false}, {429, "rate_limit", false},
	} {
		t.Run(test.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				json.NewEncoder(w).Encode(map[string]string{"error": test.code})
			}))
			defer server.Close()
			client := &Client{issuerURL: server.URL, apiURL: server.URL}
			testCredentials(t, client, time.Now().Add(-time.Minute))
			err := client.CheckConnection(context.Background(), "oaiapp_test")
			if err == nil || errors.Is(err, ErrSignInRequired) != test.invalid {
				t.Fatalf("connection = %v, want invalid=%v", err, test.invalid)
			}
		})
	}
}
