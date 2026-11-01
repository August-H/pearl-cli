package chatgpt

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
)

type identityClaims struct {
	Issuer          string          `json:"iss"`
	Subject         string          `json:"sub"`
	Email           string          `json:"email"`
	Audience        json.RawMessage `json:"aud"`
	AuthorizedParty string          `json:"azp"`
	ExpiresAt       int64           `json:"exp"`
	NotBefore       int64           `json:"nbf"`
	Nonce           string          `json:"nonce"`
}

// verifyIdentity accepts only RS256 signatures from OpenAI's published keys.
// Never use an unverified JWT's email or subject as an account identity.
func (c *Client) verifyIdentity(ctx context.Context, raw, clientID, nonce string) (identityClaims, error) {
	var claims identityClaims
	fail := errors.New("ChatGPT identity could not be verified; sign in again")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return claims, fail
	}
	decode := func(part string, value any) error {
		data, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, value)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if decode(parts[0], &header) != nil || header.Alg != "RS256" || header.Kid == "" {
		return claims, fail
	}
	var keys struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := c.getJSON(ctx, c.authURL()+"/.well-known/jwks.json", &keys); err != nil {
		return claims, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, fail
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	verified := false
	for _, key := range keys.Keys {
		if key.Kid != header.Kid || key.Kty != "RSA" || key.Use != "" && key.Use != "sig" || key.Alg != "" && key.Alg != "RS256" {
			continue
		}
		n, err := base64.RawURLEncoding.DecodeString(key.N)
		if err != nil {
			continue
		}
		e, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		exponent := 0
		for _, b := range e {
			exponent = exponent*256 + int(b)
		}
		public := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}
		if public.N.BitLen() < 2048 {
			continue
		}
		if rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature) == nil {
			verified = true
			break
		}
	}
	if !verified || decode(parts[1], &claims) != nil {
		return identityClaims{}, fail
	}
	var audiences []string
	var single string
	if json.Unmarshal(claims.Audience, &single) == nil {
		audiences = []string{single}
	} else {
		_ = json.Unmarshal(claims.Audience, &audiences)
	}
	matched := false
	for _, a := range audiences {
		matched = matched || a == clientID
	}
	now := time.Now().Unix()
	if !matched || claims.Issuer != c.authURL() || claims.Subject == "" || claims.Nonce != nonce || claims.ExpiresAt <= now || claims.NotBefore > now+30 || len(audiences) > 1 && claims.AuthorizedParty != clientID || claims.AuthorizedParty != "" && claims.AuthorizedParty != clientID {
		return identityClaims{}, fail
	}
	return claims, nil
}
