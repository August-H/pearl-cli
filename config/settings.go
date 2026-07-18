package config

type Settings struct {
	Username        string `json:"username"`
	Model           string `json:"model"`
	Max_concurrency int    `json:"max_concurrency"`
	Max_depth       int    `json:"max_depth"`
	Mode            string `json:"mode"`
}
