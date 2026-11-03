package daemon

import (
	"encoding/json"
	"regexp"
	"strings"
)

const subagentJobRequestLimit = 8

const autonomousSubagentInstructions = `You are a subagent in a Pearl autonomous session. Your report goes back to the orchestrator.

You can ask the orchestrator to spawn follow-up jobs. Describe the issue you found and the work you need done, including relevant files, evidence, and the expected result. Include up to eight request blocks in your final response, using this format for each request:
<pearl_job_request>
{"issue":"What is wrong or missing, and why it matters","task":"The specific work another job should do, with enough context to act independently"}
</pearl_job_request>

Requests reach the orchestrator when you return your final report. If you are blocked on the requested work, report your progress and the blocker now instead of waiting for another job. Otherwise, finish the independent work you can do and include the requests with your report. A request is not approval: the orchestrator may accept, revise, or decline it and chooses the model itself. Do not spawn jobs yourself or assume a requested job will run.`

type subagentJobRequest struct {
	Issue string `json:"issue"`
	Task  string `json:"task"`
}

var subagentJobRequestBlock = regexp.MustCompile(`(?s)<pearl_job_request>\s*(.*?)\s*</pearl_job_request>`)

// readSubagentJobRequests reads the full report before its middle is shortened.
// Requests have their own bounded share of the orchestrator's context.
func readSubagentJobRequests(report string, limit int) []subagentJobRequest {
	var requests []subagentJobRequest
	for _, match := range subagentJobRequestBlock.FindAllStringSubmatch(report, subagentJobRequestLimit) {
		if len(match[1]) > autonomousPromptLimit {
			continue
		}
		var request subagentJobRequest
		if err := json.Unmarshal([]byte(match[1]), &request); err != nil {
			continue
		}
		request.Issue = strings.TrimSpace(request.Issue)
		request.Task = strings.TrimSpace(request.Task)
		if request.Issue != "" && request.Task != "" {
			requests = append(requests, request)
		}
	}
	if len(requests) > 0 {
		fieldLimit := limit / (2 * len(requests))
		for index := range requests {
			requests[index].Issue = shortenMiddle(requests[index].Issue, fieldLimit)
			requests[index].Task = shortenMiddle(requests[index].Task, fieldLimit)
		}
	}
	return requests
}
