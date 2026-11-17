package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/August-H/pearl-cli/internal/store"
)

func runRespond(args []string) int {
	if len(args) >= 2 {
		return respondToJob(args[0], strings.Join(args[1:], " "))
	}
	if err := ensureDaemonRunning(); err != nil {
		return printError("Respond", err)
	}
	client, err := newDaemonClient()
	if err != nil {
		return printError("Respond", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var jobs []store.Job
	if len(args) == 1 {
		job, err := client.job(ctx, args[0])
		if err != nil {
			return printError("Respond", err)
		}
		if job.Status != store.JobWaitingInput {
			return printError("Respond", fmt.Errorf("job %q is %s; use pearl respond <job-id> \"answer\" to send a follow-up once it finishes", job.ID, job.Status))
		}
		jobs = []store.Job{job}
	} else {
		jobs, err = client.jobs(ctx)
		if err != nil {
			return printError("Respond", err)
		}
		jobs = waitingInputJobs(jobs, currentWorkspace())
		if len(jobs) == 0 {
			fmt.Println("No jobs are waiting for input in this workspace.")
			return 0
		}
	}
	job, answer, err := promptForJobResponse(jobs, os.Stdin, os.Stdout)
	if err != nil {
		return printError("Respond", err)
	}
	return respondWithClient(client, job.ID, answer)
}

func waitingInputJobs(jobs []store.Job, workspace string) []store.Job {
	var waiting []store.Job
	for _, job := range filterJobsForWorkspace(jobs, workspace) {
		if job.Status == store.JobWaitingInput {
			waiting = append(waiting, job)
		}
	}
	return waiting
}

func promptForJobResponse(jobs []store.Job, input io.Reader, output io.Writer) (store.Job, string, error) {
	reader := bufio.NewReader(input)
	selected := 0
	if len(jobs) > 1 {
		fmt.Fprintln(output, "Jobs waiting for input:")
		for index, job := range jobs {
			fmt.Fprintf(output, "  %d. %s: %s\n", index+1, job.ID, singleLineTableText(job.Question))
		}
		for {
			fmt.Fprint(output, "Choose a job number or ID: ")
			choice, err := readConfigureLine(reader)
			if err != nil {
				return store.Job{}, "", fmt.Errorf("read job selection: %w", err)
			}
			selected = -1
			for index, job := range jobs {
				if choice == job.ID {
					selected = index
					break
				}
			}
			if selected < 0 {
				if number, err := strconv.Atoi(choice); err == nil && number >= 1 && number <= len(jobs) {
					selected = number - 1
				}
			}
			if selected >= 0 {
				break
			}
			fmt.Fprintln(output, "Choose one of the listed jobs.")
		}
	}
	job := jobs[selected]
	fmt.Fprintf(output, "Job: %s\n%s\n", job.ID, job.Question)
	for {
		fmt.Fprint(output, "Answer: ")
		answer, err := readConfigureLine(reader)
		if err != nil {
			return store.Job{}, "", fmt.Errorf("read answer: %w", err)
		}
		if answer != "" {
			return job, answer, nil
		}
		fmt.Fprintln(output, "Answer cannot be empty.")
	}
}

func dashboardRespondDraft(args []string, jobs []store.Job, workspace string) (string, string, error) {
	if len(args) == 1 {
		for _, job := range jobs {
			if job.ID == args[0] && job.Status == store.JobWaitingInput {
				return "respond " + dashboardEscapeCommandArgument(job.ID) + " ", job.Question, nil
			}
		}
		return "", "", errors.New("that job is not waiting for input; use respond <job-id> \"answer\" to send a follow-up once it finishes")
	}
	waiting := waitingInputJobs(jobs, workspace)
	switch len(waiting) {
	case 0:
		return "", "No jobs are waiting for input in this workspace.", nil
	case 1:
		return "respond " + dashboardEscapeCommandArgument(waiting[0].ID) + " ", waiting[0].Question, nil
	default:
		return "respond ", "Choose a waiting job with the arrow keys and Tab, then type your answer.", nil
	}
}
