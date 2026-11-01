package chatgpt

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testCredentials(t *testing.T, c *Client, expires time.Time) {
	t.Helper()
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	err := c.withCredentials(context.Background(), func(s *credentials) error {
		*s = credentials{HostID: "urn:uuid:test-host", Active: "oaiapp_test", Accounts: []Account{{ClientID: "oaiapp_test", Issuer: c.authURL(), Subject: "user-1", Email: "user@example.com", AccessToken: "access-secret", RefreshToken: "refresh-secret", Scopes: requestedScopes, ExpiresAt: expires}}}
		return saveCredentials(s)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readCredentials(t *testing.T, c *Client) credentials {
	t.Helper()
	var result credentials
	if err := c.withCredentials(context.Background(), func(s *credentials) error { result = *s; return nil }); err != nil {
		t.Fatal(err)
	}
	return result
}

func signIdentity(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test"})
	body, _ := json.Marshal(claims)
	message := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(message))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return message + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestLoginValidatesIdentityAndReusesRegistration(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var authorization url.Values
	var endpoint string
	var exchanges int
	var deniedScope, badNonce, badSubject bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/api/accounts/oauth/token":
			exchanges++
			r.ParseForm()
			if r.Form.Get("client_id") != "oaiapp_new" || r.Form.Get("redirect_uri") != authorization.Get("redirect_uri") || r.Form.Get("resource") != resource {
				t.Errorf("unexpected token exchange: %v", r.Form)
			}
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(hash[:]) != authorization.Get("code_challenge") {
				t.Error("PKCE verifier mismatch")
			}
			nonce := authorization.Get("nonce")
			if badNonce {
				nonce = "wrong"
			}
			subject := "user-1"
			if badSubject {
				subject = "user-2"
			}
			id := signIdentity(t, key, map[string]any{"iss": endpoint, "sub": subject, "aud": "oaiapp_new", "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce, "email": "user@example.com"})
			scope := requestedScopes
			if deniedScope {
				scope = "openid email profile"
			}
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", IDToken: id, TokenType: "Bearer", ExpiresIn: 3600, Scope: scope})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	endpoint = server.URL
	c := &Client{issuerURL: server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var hostID, firstState string
	open := func(address string) error {
		u, _ := url.Parse(address)
		authorization = u.Query()
		if hostID == "" {
			hostID = authorization.Get("ext_agent_host_id")
			firstState = authorization.Get("state")
		} else if hostID != authorization.Get("ext_agent_host_id") || firstState == authorization.Get("state") {
			t.Error("host changed or state reused")
		}
		if strings.Contains(address, "id_token_hint") {
			t.Error("CLI authorization URL contains a retained credential")
		}
		callback, _ := url.Parse(authorization.Get("redirect_uri"))
		if callback.Hostname() != "127.0.0.1" || callback.Path != "/auth/callback" {
			t.Errorf("callback = %s", callback)
		}
		q := url.Values{"code": {"code-secret"}, "state": {"bad-state"}, "client_id": {"oaiapp_new"}}
		callback.RawQuery = q.Encode()
		resp, err := http.Get(callback.String())
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Error("accepted invalid state")
		}
		q.Set("state", authorization.Get("state"))
		callback.RawQuery = q.Encode()
		resp, err = http.Get(callback.String())
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := c.Login(ctx, "", open); err != nil {
		t.Fatal(err)
	}
	if authorization.Get("client_id") != dynamicClient || authorization.Get("agent_name_hint") != "Pearl" {
		t.Fatal("did not register new public client")
	}
	saved := readCredentials(t, c)
	if saved.Active != "oaiapp_new" || saved.Accounts[0].Subject != "user-1" || saved.Accounts[0].AccessToken != "new-access" {
		t.Fatalf("account did not save validated identity")
	}
	info, err := os.Stat(filepath.Join(os.Getenv("PEARL_CONFIG_DIR"), "chatgpt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("credential mode = %o", info.Mode().Perm())
	}
	if err := c.Login(ctx, "oaiapp_new", open); err != nil {
		t.Fatal(err)
	}
	if authorization.Get("client_id") != "oaiapp_new" || authorization.Has("agent_name_hint") {
		t.Error("reauthorization did not reuse registration")
	}
	for _, failure := range []string{"nonce", "identity", "scope"} {
		badNonce = failure == "nonce"
		badSubject = failure == "identity"
		deniedScope = failure == "scope"
		if err := c.Login(ctx, "oaiapp_new", open); err == nil {
			t.Errorf("accepted invalid %s", failure)
		}
		current := readCredentials(t, c)
		if current.Accounts[0].Subject != saved.Accounts[0].Subject || current.Accounts[0].IDToken == "" {
			t.Errorf("failed sign-in replaced previous identity")
		}
	}
	if exchanges != 5 {
		t.Errorf("exchanges = %d", exchanges)
	}
}

func TestRefreshIsSerializedAndPinnedAccountsDoNotSwitch(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_id") != "oaiapp_test" || r.Form.Get("refresh_token") != "refresh-secret" || r.Form.Get("resource") != resource || r.Form.Has("scope") {
			t.Error("invalid refresh request")
		}
		count.Add(1)
		json.NewEncoder(w).Encode(tokenResponse{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", TokenType: "Bearer", ExpiresIn: 3600})
	}))
	defer server.Close()
	c := &Client{issuerURL: server.URL}
	testCredentials(t, c, time.Now().Add(-time.Hour))
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := c.AccessToken(ctx)
			if err != nil || value != "rotated-access" {
				t.Errorf("refresh = %q, %v", value, err)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("refreshes = %d", count.Load())
	}
	saved := readCredentials(t, c)
	if saved.Accounts[0].RefreshToken != "rotated-refresh" || saved.Accounts[0].Scopes != requestedScopes {
		t.Error("rotation was not persisted")
	}
	pinned, err := c.Pin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.withCredentials(ctx, func(s *credentials) error {
		s.Accounts = append(s.Accounts, Account{ClientID: "other", AccessToken: "other-access", Scopes: requestedScopes, ExpiresAt: time.Now().Add(time.Hour)})
		s.Active = "other"
		return saveCredentials(s)
	}); err != nil {
		t.Fatal(err)
	}
	value, err := pinned.AccessToken(ctx)
	if err != nil || value != "rotated-access" {
		t.Fatal("running job changed account")
	}
}

func TestCancelledAndRejectedCallbacksNeverExchange(t *testing.T) {
	t.Setenv("PEARL_CONFIG_DIR", t.TempDir())
	c := NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := c.Login(ctx, "", func(address string) error {
		u, _ := url.Parse(address)
		callback, _ := url.Parse(u.Query().Get("redirect_uri"))
		callback.RawQuery = url.Values{"state": {u.Query().Get("state")}, "error": {"access_denied"}}.Encode()
		resp, err := http.Get(callback.String())
		if err == nil {
			resp.Body.Close()
		}
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("login error = %v", err)
	}
	if len(readCredentials(t, c).Accounts) != 0 {
		t.Error("declined sign-in created an account")
	}
	cancel()
	if err := c.Login(ctx, "", func(string) error { return nil }); err == nil {
		t.Fatal("canceled login succeeded")
	}
}

func TestLogoutRevokesAndRetainsRegistration(t *testing.T) {
	var endpoint string
	var revoked bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fmt.Fprintf(w, `{"revocation_endpoint":%q}`, endpoint+"/revoke")
			return
		}
		r.ParseForm()
		if r.Form.Get("token") != "refresh-secret" || r.Form.Get("client_id") != "oaiapp_test" || r.Form.Get("token_type_hint") != "refresh_token" {
			t.Error("bad revocation request")
		}
		revoked = true
	}))
	defer server.Close()
	endpoint = server.URL
	c := &Client{issuerURL: endpoint}
	testCredentials(t, c, time.Now().Add(time.Hour))
	if err := c.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved := readCredentials(t, c)
	if !revoked || saved.Active != "oaiapp_test" || saved.HostID == "" || saved.Accounts[0].Subject == "" || saved.Accounts[0].AccessToken != "" || saved.Accounts[0].RefreshToken != "" {
		t.Fatal("logout did not retain registration and clear tokens")
	}
}

func TestModelsUseAccountCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer access-secret" {
			t.Error("wrong catalog or credential")
		}
		io.WriteString(w, `{"models":[{"slug":"available-model","display_name":"Available","visibility":"list"},{"slug":"hidden-model","visibility":"hide"}]}`)
	}))
	defer server.Close()
	c := &Client{apiURL: server.URL}
	testCredentials(t, c, time.Now().Add(time.Hour))
	models, err := c.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "available-model" {
		t.Fatalf("catalog=%v, %v", models, err)
	}
}
