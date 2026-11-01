package cli

import (
	"github.com/August-H/pearl-cli/internal/store"
	"strings"
)

func jobVerificationLabel(job store.Job) string {
	switch job.Verification {
	case "passed", "failed", "running":
		return job.Verification
	}
	return "not run"
}

func jobRunDetails(job store.Job) string {
	var parts []string
	if job.Provider != "" {
		parts = append(parts, job.Provider)
	}
	if job.Model != "" {
		parts = append(parts, job.Model)
	}
	if job.Harness != "" {
		parts = append(parts, "Harness: "+job.Harness)
	}
	parts = append(parts, "Tests: "+jobVerificationLabel(job))
	return dashboardText(strings.Join(parts, " · "))
}
