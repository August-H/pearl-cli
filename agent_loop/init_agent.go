package agentloop

import "github.com/August-H/pearl-cli/openrouter_request"

// Init_agent asks the configured model to complete prompt using the allowed
// project tools.
func Init_agent(prompt string) (string, error) {
	return openrouter_request.Init_agent(prompt)
}
