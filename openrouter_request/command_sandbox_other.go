//go:build !darwin && !linux

package openrouter_request

func sandboxCommand(commandSandbox, string, []string, string) (string, []string, error) {
	return "", nil, errNoSandbox
}
