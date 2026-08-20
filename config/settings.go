package config

type Settings struct {
	Username                 string   `json:"username"`
	Model                    string   `json:"model"`
	Max_concurrency          int      `json:"max_concurrency"`
	Max_depth                int      `json:"max_depth"`
	Mode                     string   `json:"mode"`
	Max_job_seconds          int      `json:"max_job_seconds"`
	Max_file_bytes           int64    `json:"max_file_bytes"`
	Approved_workspace_roots []string `json:"approved_workspace_roots"`
}
