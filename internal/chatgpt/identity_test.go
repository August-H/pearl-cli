package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIdentityRejectsForgedExpiredAndWrongAudienceTokens(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kid": "test", "kty": "RSA", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	defer server.Close()
	c := &Client{issuerURL: server.URL}
	for _, test := range []string{"issuer", "audience", "expiry", "nonce", "signature", "unsigned"} {
		t.Run(test, func(t *testing.T) {
			claims := map[string]any{"iss": server.URL, "sub": "user-1", "aud": "oaiapp_test", "nonce": "expected", "exp": time.Now().Add(time.Hour).Unix()}
			switch test {
			case "issuer":
				claims["iss"] = "https://other.example"
			case "audience":
				claims["aud"] = "other-client"
			case "expiry":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "nonce":
				claims["nonce"] = "wrong"
			}
			token := signIdentity(t, key, claims)
			if test == "signature" {
				parts := strings.Split(token, ".")
				parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"forged"}`))
				token = strings.Join(parts, ".")
			}
			if test == "unsigned" {
				parts := strings.Split(token, ".")
				parts[0] = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"test"}`))
				token = strings.Join(parts, ".")
			}
			if _, err := c.verifyIdentity(context.Background(), token, "oaiapp_test", "expected"); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}
