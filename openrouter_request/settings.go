package openrouter_request

type Settings struct {
	AllowedCommands           []string `json:"allowed_commands,omitempty"`
	Username                  string   `json:"username"`
	Model                     string   `json:"model"`
	Provider                  string   `json:"provider,omitempty"`
	ChatGPTModel              string   `json:"chatgpt_model,omitempty"`
	ClaudeModel               string   `json:"claude_model,omitempty"`
	AntigravityModel          string   `json:"antigravity_model,omitempty"`
	Max_concurrency           int      `json:"max_concurrency"`
	Max_depth                 int      `json:"max_depth"`
	Mode                      string   `json:"mode"`
	Max_job_seconds           int      `json:"max_job_seconds"`
	Max_file_bytes            int64    `json:"max_file_bytes"`
	Context_compaction_tokens int      `json:"context_compaction_tokens"`
	Context_keep_tokens       int      `json:"context_keep_tokens"`
	Approved_workspace_roots  []string `json:"approved_workspace_roots"`
}

type Reasoning struct {
	Enabled bool `json:"enabled"`
}

type APIError struct {
	Code    any    `json:"code"`
	Message string `json:"message"`
}
