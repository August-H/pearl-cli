package resource

import (
	"bytes"
	"encoding/json"
	"io"
)

// ResponseUsage reads usage from OpenRouter and Responses API stream events.
func ResponseUsage(line []byte) (Usage, bool) {
	line = bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(line), []byte("data:")))
	var payload struct {
		Usage    *responseUsage `json:"usage"`
		Response *struct {
			Usage *responseUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &payload) != nil {
		return Usage{}, false
	}
	u := payload.Usage
	if payload.Response != nil && payload.Response.Usage != nil {
		u = payload.Response.Usage
	}
	if u == nil {
		return Usage{}, false
	}
	tokens := u.Total
	if tokens == 0 {
		tokens = u.Input + u.Output + u.Prompt + u.Completion
	}
	return Usage{Tokens: tokens, CostUSD: u.Cost}, true
}

type responseUsage struct {
	Total      int64   `json:"total_tokens"`
	Input      int64   `json:"input_tokens"`
	Output     int64   `json:"output_tokens"`
	Prompt     int64   `json:"prompt_tokens"`
	Completion int64   `json:"completion_tokens"`
	Cost       float64 `json:"cost"`
}

// UsageReader preserves the upstream bytes while recording the final usage
// event. Large content lines are passed through without buffering them.
type UsageReader struct {
	Body     io.ReadCloser
	Report   func(Usage) error
	line     []byte
	skipping bool
	reported bool
	failure  error
}

func (r *UsageReader) Read(p []byte) (int, error) {
	if r.failure != nil {
		return 0, r.failure
	}
	n, err := r.Body.Read(p)
	data := p[:n]
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		part := data
		if index >= 0 {
			part = data[:index]
		}
		if len(r.line)+len(part) > 1<<20 {
			r.skipping = true
			r.line = nil
		}
		if !r.skipping {
			r.line = append(r.line, part...)
		}
		if index < 0 {
			break
		}
		r.finishLine()
		data = data[index+1:]
	}
	if err == io.EOF {
		r.finishLine()
	}
	if r.failure != nil {
		err = r.failure
	}
	return n, err
}

func (r *UsageReader) finishLine() {
	if !r.skipping && !r.reported {
		if usage, ok := ResponseUsage(r.line); ok {
			r.reported = true
			r.failure = r.Report(usage)
		}
	}
	r.line = r.line[:0]
	r.skipping = false
}
func (r *UsageReader) Close() error { return r.Body.Close() }
