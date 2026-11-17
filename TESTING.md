# Testing Pearl

Pearl's tests protect command behavior, durable job history, agent execution,
and workspace boundaries. Use them when replacing or optimizing the CLI,
daemon, storage, or provider integrations.

```sh
make test              # vet and all tests, including functional tests
make test-functional   # only the compiled CLI's functional contracts, uncached
make test-race         # vet and the entire suite with race detection, uncached
```

The equivalent commands work without Make:

```sh
go vet ./...
go test ./... -count=1 -timeout 5m
go test -race ./... -count=1 -timeout 5m
```

Go and a C compiler are required because SQLite uses cgo. The command-execution
test also runs a small Go module with no external dependencies. Download the
repository's Go dependencies before running offline. No provider login, API key,
installed agent CLI, or paid inference is required.

The existing GitHub Actions matrix runs `go test -race ./...` on Linux, macOS,
and Windows, so it includes the functional package automatically.

## Functional test design

[`tests/functional`](tests/functional) builds the real `cmd/pearl` executable
once, then invokes commands as child processes. Each test gets its own config,
SQLite database, socket, and workspace. Child CLIs have a temporary home and a
small environment without inherited account credentials.

Most execution tests start the real daemon in the test process with
`daemon.OpenRouterRunner`. Only its model HTTP transport changes: requests to
OpenRouter's completion endpoint go to a local fake service. Unexpected outbound
requests fail. The service returns scripted model messages and tool calls;
Pearl itself handles those calls, executes commands, records history, and
captures changes. This keeps tests repeatable while checking the actual agent
loop. The background-daemon test separately exercises the compiled executable's
process start, restart, and stop behavior without running inference.

Crash recovery runs the production daemon in a disposable test subprocess with
the same local provider transport, then kills it without shutdown. The viewer
test kills a compiled CLI process while the daemon continues running. When the
suite runs with `-race`, it also builds the child CLI with race detection.

[`harness_test.go`](tests/functional/harness_test.go) contains this setup and
small types for the documented JSON fields. Tests accept additional fields.
They check results, exit status, output streams, and persisted history without
depending on private CLI helpers, database tables, SQL statements, exact terminal
frames, or fixed event counts. Timing checks wait for observable state with a
deadline. Tests stay serial because the agent HTTP transport and config
environment are process-wide.

## Added command tests

These tests are in [`cli_test.go`](tests/functional/cli_test.go). Names below
match the test functions after the `Test` prefix.

| Test | What it protects and why |
|---|---|
| `HelpAndVersionWorkWithoutStartingDaemon` | Help across the command tree and all version aliases works without creating runtime files. Version JSON contains version, commit, and date. Users can inspect the CLI before setup. |
| `UsageErrorsHaveNonzeroExitAndDoNotStartDaemon` | Invalid arguments across commands fail with a diagnostic and leave the daemon stopped. Argument parsing cannot silently turn mistakes into work. |
| `OfflineJSONQueriesReturnOneErrorOnStderr` | Disconnected inspection commands emit exactly one JSON error on stderr, no stdout, and a nonzero exit code. They never start a daemon. Scripts can distinguish data from failures. |
| `ConfigureAndModelSelectionPreserveUserPolicy` | Interactive configuration saves the key and free model. Direct and free model selection persist while preserving command permissions, workspace roots, file-size limits, and outside-access policy. Changing a model must not reset user policy. |
| `SkillsFilesAreDiscoverableWithoutDaemon` | The skills path is inside the test config. Both Markdown files and folders containing `SKILL.md` appear with their names and descriptions. No file manager opens. |
| `JobCreationWorkspaceScopingAndValidation` | Named jobs preserve quoted, Unicode, and multiline prompts. Lists include nested workspaces, exclude unrelated workspaces, and honor `--all`. Duplicate/invalid names and missing directories cannot add jobs. Creation and pending cancellation make no model requests. |
| `FoldersMoveJobsAndRemovalKeepsThem` | Folder assignment sets a job's workspace, editing the folder moves pending jobs, and removing it keeps the jobs and their last workspace. |
| `SchedulesAreScopedDurableAndRemovable` | Schedule name, prompt, interval, workspace filtering, `--all`, removal, and persistence survive a daemon restart. Future schedules do not run early. |
| `DaemonRestartKeepsPendingAndCancelledJobs` | Stopping and reopening the daemon preserves job states and prompts. Both status JSON commands include resource information. |
| `BackgroundDaemonStartsRestartsAndStopsThroughCLI` | The compiled CLI starts an actual background daemon, tolerates repeated starts/stops, and preserves a job through an actual daemon process restart. |

## Added execution tests

These tests are in [`workflows_test.go`](tests/functional/workflows_test.go).

| Test | What it protects and why |
|---|---|
| `RunUsesRealToolsAndSavesReviewableHistory` | A scripted model reads, patches, creates directories/files, writes, lists, and searches real workspace files. The command streams its answer, saves the transcript/tool history/status history, records real diffs, supports later attachment, and retains the report across restart. |
| `WaitingJobYieldsQueueAndResumesAfterRestart` | A job edits a file and asks a question. Another job can run while it waits. The question survives restart, `respond` continues the same conversation, and completed tool work stays recorded once. |
| `IncompleteRunFailsAndRetryKeepsCompletedTools` | A token-limited response fails the job. After restart, retry receives the saved tool result, streams only the new attempt, and captures a fresh diff without attributing the earlier edit to itself. |
| `FollowUpContinuesSavedConversation` | A follow-up after restart sends the original task, previous answer, and new user message back to the model and retains both answers in history. |
| `CancellationStopsActiveRequestAndReleasesQueue` | Cancelling a running job cancels its HTTP request and lets the next queued job finish. Ordinary jobs cannot execute simultaneously. |
| `ArchiveHidesJobButPreservesItsReport` | Archiving through the daemon API removes a job from the normal board, exposes it in `archive --json`, and keeps its result and transcript after restart. The terminal archive key is covered by existing CLI tests. |
| `UnsafeToolRequestsCannotChangeOutsideFilesOrSecrets` | Parent-path writes, protected `.env` reads, and disabled commands return failed tool results. A secret canary never reaches the model, and an outside file remains intact. |
| `APIRejectsInvalidRequestsWithoutMutatingJobs` | Malformed bodies, missing workspaces, wrong HTTP methods, invalid responses, and unknown routes return errors while leaving an existing pending job unchanged. Handled errors use JSON. |
| `CommandExecutionTracksTestsAndInvalidatesAfterCodeEdits` | `run_command` executes a real passing `go test`, withholds a credential canary, clears the passed state after a code patch, and records the subsequent failing test. Model prose claiming success cannot override the exit status. |
| `AutonomousSessionCreatesJobReviewsReportAndFinishes` | The autonomous API runs a real orchestrator turn, creates a job on the assigned model, supplies its completed report to the next turn, and saves the final summary and both turns. |
| `UsageCapsStopDispatchAndSurviveRestart` | Separate token-cap and spending-cap cases record provider usage, fail the exhausted job, retain usage across restart, and keep subsequent work queued without calling the model. |
| `DueScheduleRunsAutomaticallyAndSkipsOverlappingFirings` | A due interval dispatches work without a `run` command. While that run is held open, the next due interval advances the schedule without creating another job. Removing the schedule lets the single existing job finish. |

## Added failure and recovery tests

These tests are in [`failures_test.go`](tests/functional/failures_test.go) and
[`recovery_test.go`](tests/functional/recovery_test.go).

| Test | What it protects and why |
|---|---|
| `ProviderFailuresReleaseQueueAndRemainRetryable` | Six cases cover HTTP authentication errors, rate limits, outages, explicit stream errors, truncated streams, and malformed stream JSON. Each records a failed job with a diagnostic, gives `attach` a nonzero exit status, releases already queued work, survives restart, and succeeds on explicit retry. A provider failure must never look like successful work or wedge the queue. |
| `RejectedJobActionsPreserveHistoryAndQueuedCancellationPreventsDispatch` | Duplicate run/retry, unsolicited responses, and archive requests against running or queued jobs fail without changing their reports. Cancelling queued work prevents inference and survives restart. Archived jobs reject run/retry/follow-up while keeping their saved report. Invalid actions must not erase history or create unexpected work. |
| `SavedSkillsReachAgentAndLoadInstructionsOnDemand` | A saved skill's name and description reach the model, while its full instructions load through `read_skill` only when requested. A missing skill returns a failed tool result without ending the run. Case-insensitive lookup works, and the loaded instructions and tool history survive restart. Discoverable skills must also work inside actual agent runs. |
| `KilledDaemonRecoversCheckpointDiffAndQueue` | Kills a daemon process after a real file patch has been checkpointed and the next model request is waiting. Restart marks the job interrupted, recovers its open change report, and dispatches previously queued work. Explicit retry receives the saved tool result, keeps the file edit recorded once, and adds a separate change report without repeating the earlier diff. Shutdown cleanup cannot hide a crash-recovery failure. |
| `DisconnectingViewerKeepsJobRunningAndAllowsReattach` | Kills a `run` viewer after it receives live output. The daemon keeps the job running; a new `attach` viewer receives the earlier fragment and the later completion exactly once. The result remains complete, and reconnecting makes no extra model request. Closing a terminal must not cancel or duplicate background work. |

## Existing coverage retained

The new tests supplement the repository's existing tests. These are the main
places to look when a refactor changes more specific behavior:

| Functionality | Existing test locations |
|---|---|
| Dashboard commands, suggestions, workspace filtering, composer editing/paste/newlines, scrolling, keyboard navigation, job inspection, archive actions, model picker, autonomous form and live view | `cli/*_test.go`, especially `dashboard`, `composer`, `jobs_list_tui`, `jobs_view_tui`, `model_tui`, `autonomous_form`, and `autonomous_tui` |
| Account login, token refresh/revocation, account isolation, provider/model preferences, model catalogs/cache, usage displays, subscription checks | `cli/auth_test.go`, `cli/accounts_test.go`, `cli/model_test.go`, `cli/usage_test.go`, `cli/subscriptions_test.go`, `internal/chatgpt/*_test.go`, `internal/daemon/agent_models_test.go`, `internal/daemon/usage_test.go`, `internal/daemon/subscriptions_test.go` |
| Codex, Claude Code, Antigravity, billing labels, external-agent events and checkpoints | `internal/daemon/harness_test.go`, `codex_test.go`, `codex_proxy_test.go`, `claude_test.go`, `antigravity_test.go` |
| Durable queue, interrupted-run recovery, migrations, checkpoint/tool journal, schedules, folders, autonomous sessions | `internal/store/*_test.go`, `internal/daemon/server_test.go` |
| Exclusive workspace locks, nested directories and aliases, global/session concurrency and budgets | `internal/store/workspace_lock_test.go`, `internal/store/resources_test.go`, `internal/daemon/resources_test.go`, `internal/resource/usage_test.go` |
| Autonomous model allowlists, report-driven follow-ups, clarification, cancellation, turn retries and silence handling | `internal/daemon/autonomous_test.go`, `orchestrator_response_test.go`, `subagent_requests_test.go`, `internal/store/autonomous_response_test.go` |
| Stream completion/errors, ChatGPT Responses history repair, context compaction, tool-call replay and follow-ups | `openrouter_request/stream_test.go`, `chatgpt_test.go`, `context_compaction_test.go`, `user_input_test.go`, `budget_test.go` |
| File-size limits, atomic replacements, patch ambiguity, pagination/ignore rules, protected paths, links, outside-access approvals | `agent_functions/*_test.go`, `openrouter_request/path_safety_test.go`, `outside_access_test.go`, `workspace_tools_test.go`, `internal/workspacepath/*_test.go` |
| Command allowlists, reduced environment, sandboxing, time/output limits, cancellation and child cleanup | `openrouter_request/command_policy_test.go`, `command_environment_test.go`, `workspace_tools_test.go` |
| Verification state, snapshots and per-run diffs | `internal/verification/verification_test.go`, `internal/store/verification_test.go`, `internal/workspacechanges/*_test.go`, `internal/daemon/changes_test.go`, `cli/changes_test.go` |
| Self-update version selection, asset selection, checksums, atomic application and rollback paths | `cli/update_test.go`, `cli/update_error_test.go` |
| Platform-specific paths, daemon locks and packaged CLI startup | `internal/pearlpaths/paths_test.go`, `internal/daemon/server_test.go`, `testenv/smoke.sh`, `testenv/smoke.ps1` |

## Refactoring rules

Preserve the behaviors above while changing implementation. You can reorganize
packages, replace the terminal renderer, change database queries, or optimize
queue dispatch without updating the functional expectations. If exported daemon
setup changes, update the small setup adapter in `harness_test.go`.

Keep command syntax, exit-status meaning, JSON field meaning, persisted user
data, conversation continuity, cancellation, and workspace restrictions stable.
Adding JSON fields is compatible. Changing a documented behavior needs an
explicit decision and a corresponding test/documentation change. Do not weaken
an assertion just to make a refactor pass.

Add a regression case at the narrowest useful level, then add a functional test
when the bug crosses command, daemon, storage, or agent boundaries. Avoid exact
terminal snapshots, wall-clock performance thresholds, and assertions about
private helpers in this package. Use separate benchmarks for optimization work.

## Limits of the suite

These tests cover Pearl's behavior with controlled provider responses. They do
not validate live provider availability, model quality, browser OAuth consent,
an installed agent's current compatibility, native directory dialogs, terminal
emulator rendering, OS login-service installation, or a production release
download. Existing tests simulate many of those integrations; hands-on checks
are still needed when changing them. The functional archive/autonomous cases
use the local API for actions normally launched from interactive screens.

An all-green run supports a refactor of the covered behavior. It is not a claim
that every possible provider response or platform configuration was tested.
