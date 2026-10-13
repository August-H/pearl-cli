package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

const (
	codexOpenRouterURL = "https://openrouter.ai"
	// codexSniffLineBytes caps how much of one response line is buffered while
	// looking for the model. The model appears in the first, small events.
	codexSniffLineBytes = 1 << 20
)

// codexModelProxy forwards Codex's OpenRouter requests and reports the model
// named in each response. Codex does not say which model OpenRouter routed a
// request to, so this is how Codex jobs record the exact model used.
type codexModelProxy struct {
	server   *http.Server
	listener net.Listener
}

func startCodexModelProxy(upstream string, report func(model string)) (*codexModelProxy, error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			// Without this, responses could arrive compressed and unreadable.
			request.Out.Header.Del("Accept-Encoding")
		},
		FlushInterval: -1,
		ModifyResponse: func(response *http.Response) error {
			response.Body = &codexModelSniffer{body: response.Body, report: report}
			return nil
		},
	}
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return &codexModelProxy{server: server, listener: listener}, nil
}

func (p *codexModelProxy) baseURL() string {
	return "http://" + p.listener.Addr().String() + "/api/v1"
}

func (p *codexModelProxy) close() {
	_ = p.server.Close()
}

// codexModelSniffer passes a response body through unchanged while reading the
// model from each JSON or server-sent event line.
type codexModelSniffer struct {
	body     io.ReadCloser
	report   func(string)
	line     []byte
	skipping bool
}

func (s *codexModelSniffer) Read(buffer []byte) (int, error) {
	count, err := s.body.Read(buffer)
	data := buffer[:count]
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		if index < 0 {
			s.appendLine(data)
			break
		}
		s.appendLine(data[:index])
		s.finishLine()
		data = data[index+1:]
	}
	if err == io.EOF {
		s.finishLine()
	}
	return count, err
}

func (s *codexModelSniffer) Close() error {
	return s.body.Close()
}

func (s *codexModelSniffer) appendLine(data []byte) {
	if s.skipping {
		return
	}
	if len(s.line)+len(data) > codexSniffLineBytes {
		s.skipping = true
		s.line = s.line[:0]
		return
	}
	s.line = append(s.line, data...)
}

func (s *codexModelSniffer) finishLine() {
	if !s.skipping {
		if model := codexResponseModel(s.line); model != "" {
			s.report(model)
		}
	}
	s.line = s.line[:0]
	s.skipping = false
}

func codexResponseModel(line []byte) string {
	line = bytes.TrimSpace(line)
	line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(line) == 0 || line[0] != '{' {
		return ""
	}
	var payload struct {
		Model    string `json:"model"`
		Response *struct {
			Model string `json:"model"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &payload) != nil {
		return ""
	}
	if payload.Response != nil && payload.Response.Model != "" {
		return payload.Response.Model
	}
	return payload.Model
}
