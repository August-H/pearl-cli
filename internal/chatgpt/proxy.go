package chatgpt

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// Proxy keeps renewable OAuth credentials in Pearl while Codex receives only
// an ephemeral loopback key. Every upstream request reloads/refreshed credentials.
type Proxy struct {
	server   *http.Server
	listener net.Listener
	Key      string
}

func (c *Client) StartProxy(ctx context.Context) (*Proxy, error) {
	key, err := randomValue()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &Proxy{listener: listener, Key: key}
	p.server = &http.Server{ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+key)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&payload); err != nil {
			http.Error(w, "invalid Responses request", 400)
			return
		}
		if err := PrepareRequest(payload); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		token, err := c.AccessToken(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 401)
			return
		}
		body, err := json.Marshal(payload)
		if err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), "POST", c.BaseURL()+"/responses", bytes.NewReader(body))
		if err != nil {
			http.Error(w, "invalid upstream request", 500)
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		client := *c.httpClient()
		client.Timeout = 0
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "ChatGPT request failed", 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		buffer := make([]byte, 32<<10)
		for {
			n, err := resp.Body.Read(buffer)
			if n > 0 {
				if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					panic(http.ErrAbortHandler)
				}
				return
			}
		}
	})}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *Proxy) BaseURL() string { return "http://" + p.listener.Addr().String() + "/v1" }
func (p *Proxy) Close()          { _ = p.server.Close() }
