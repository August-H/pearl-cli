# Pearl CLI

Pearl is an agent task manager. You can assign work as jobs, close the terminal, return later, and get information on the agent changes. Currently only supports OpenRouter. 
## What Pearl does

- Creates named jobs for a specific project directory.
- Runs jobs in a durable queue, one at a time.
- Saves transcripts, tool activity, changed files, errors, and user responses.
- Compacts long-running model context automatically without trimming the saved transcript.
- Pauses a job when the agent needs input without blocking the rest of the queue.
- Provides a terminal dashboard for running, retrying, answering, reviewing, and
  archiving jobs.
- Supports recurring jobs through interval schedules.
- Runs autonomous sessions where a coordinator creates subagent jobs, reviews
  their results, and assigns follow-up work.

Pearl only calls the model while a job is running. An idle daemon does not use
model tokens.

## Install

One command installs the latest release binary, verifies its checksum, and
prepares your PATH. No compiler or repository clone needed.

macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/August-H/pearl-cli/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/August-H/pearl-cli/main/install.ps1 | iex
```

The macOS and Linux script installs to `~/.local/bin` and tells you how to add
it to `PATH` if needed. The Windows script installs to `%USERPROFILE%\bin` and
adds that directory to your user PATH automatically.

Then run the setup wizard and enter your OpenRouter API key:

```bash
pearl configure
```

Choose `free` to use `openrouter/free`, or choose `custom` and enter an
OpenRouter model ID.

Open the dashboard:

```bash
pearl
```

Commands entered in the dashboard omit the `pearl` prefix. Create your first
job from the current directory:

```text
job -n "fix tests" "inspect this repository and fix the failing tests"
jobs
```

In the jobs list, select `fix tests` and press Space. Pearl fills the command box
with `run fix\ tests`. Press Enter to start it. When it finishes, open `jobs`
again and press Enter on the job to inspect its transcript, changed files, and
tool activity.

The same workflow works without the dashboard:

```bash
pearl job -n "fix tests" "inspect this repository and fix the failing tests"
pearl run "fix tests"
pearl jobs
```

For a larger goal, start autonomous mode from the project directory:

```bash
pearl autonomous "audit the release, fix the problems you find, and verify the result"
```

Pearl opens a live TUI and keeps the coordinator in the daemon. Press `q` to
detach. The session and its child jobs continue running.

### Build from source

You need Go and a C toolchain (SQLite uses cgo). From the repository root:

```bash
make install
```

`make install` embeds the version, commit, and build date into the binary and
places it in `$(go env GOPATH)/bin`. Plain `go build -o pearl ./cmd/pearl`
still works but produces a binary that reports `version dev`.

To place the binary somewhere specific instead:

```bash
mkdir -p "$HOME/.local/bin"
install -m 755 ./pearl "$HOME/.local/bin/pearl"
```

## Configuration and storage

Pearl stores its API key, settings, SQLite database, socket, and log under the
platform user config directory in a `pearl` subdirectory. Set
`PEARL_CONFIG_DIR` to use a different location and `PEARL_SOCKET` to override
only the Unix socket path.

`configure` asks for the OpenRouter API key and then offers the free router or
a custom model ID. It stores the selection in the durable `settings.json`. The
same wizard runs inside the dashboard command box when you enter `configure`.

`pearl model` opens an interactive picker with every current OpenRouter model,
grouped by provider with older generations hidden (only Fable 5.1, not Fable 5).
The Free Tier option at the top selects `openrouter/free`. Non-interactive
flags cover scripts and the dashboard: `pearl model --list`,
`pearl model --set <model-id>`, and `pearl model --free`.

## Jobs and directories

Create a job from the directory Pearl should work in, then run it by ID:

```bash
pearl job -n "fix tests" "inspect this repository and fix the tests"
pearl run "fix tests"
```

`pearl job` saves the prompt with a `pending` status and does not run it.
`pearl run <job-id>` queues a pending job or retries a finished job, starts the
background daemon when needed, and streams the result. Add `--detach` before
the ID to run it in the background. Put `-n "name"` before the prompt to use
that name as the job ID. Custom IDs can contain up to 20 characters and cannot
begin with a hyphen.

Add `--directory` or `-d` to choose another workspace with a native folder
picker. Pearl validates the selection before saving the job. macOS uses the
system folder dialog. Windows uses its PowerShell folder browser. Linux uses
Zenity or KDialog when either is installed.

```bash
pearl job --directory -n "fix docs" "update the documentation"
```

The durable queue, schedules, and daemon lifecycle remain available as advanced
operations:

```bash
pearl
pearl help
pearl job -n "login fix" "fix the login flow"
pearl run --detach "login fix"
pearl jobs
pearl jobs view "login fix"
pearl archive
pearl dashboard
pearl model
pearl attach <job-id>
pearl respond <job-id> "your answer"
pearl cancel <job-id>
pearl retry <job-id>
pearl autonomous "goal"
pearl autonomous --resume <session-id>
```

Running `pearl` without a command opens the dashboard.

`pearl jobs` lists jobs for the current directory and its subfolders. Use
`--all` to list every workspace; the listing then adds a WORKSPACE column. In
the selectable list, use the arrow
keys or `j` and `k` to move between jobs, Page Up and Page Down to move faster,
and Enter to open the selected job. Space preloads `run <job-id>` for pending
jobs, `retry <job-id>` for finished jobs, or `respond <job-id> ` for jobs waiting
for input. The list closes and fills the terminal prompt without running the
command. Type the response when needed, then press Enter to confirm. Press `d`
to archive the selected job, then confirm with `y` or Enter. Pearl keeps the
job transcript, changed files, tool activity, and timestamps. Queued and running
jobs must finish or be cancelled before archival.

`pearl archive` opens the archived job list for the current directory. Add
`--all` to browse archived jobs from every workspace. Navigate it like `pearl
jobs` and press Enter to open the selected job. Archived jobs do not appear on
the normal job board, in run autocomplete, or in the daemon queue.

The job report is split into expandable sections. Use the arrow keys to select
a section, Enter or Space to expand it, `j` and `k` or Page Up and Page Down to
scroll, `a` to expand everything, `c` to collapse everything, and `q` to exit.
Piped output remains plain text.

`pearl status` shows the daemon state, queued count, and the IDs of every job
waiting for input across all workspaces, so a paused job in another repository
is never hidden.

`Ctrl-C` detaches the current terminal from an attached job; it does not cancel
the job. Use `pearl cancel` when cancellation is intended.

## Updating Pearl

```bash
pearl update --check   # show the latest release without installing
pearl update           # download, verify, and install the latest release
```

`pearl update` fetches the newest release from the GitHub repository, verifies
its SHA-256 checksum against a companion `.sha256` asset, and swaps the binary
atomically. The previous executable is kept next to the new one as `pearl.old`
for rollback. After installing, Pearl restarts an idle daemon so it picks up
the new version; if a job is running, it prints the `pearl daemon restart`
command to run once the queue is quiet.

Builds made without release metadata (plain `go build`, commit-pinned builds)
report no comparable version and refuse to self-update unless `--force` is
given.

Releases are cut by pushing a tag such as `v0.2.0`. The GitHub Actions release
workflow builds binaries for darwin (arm64, amd64), linux (amd64, arm64), and
windows (amd64) with their SHA-256 checksums and attaches them to the release.
The Makefile embeds the same version, commit, and date locally via
`make build` or `make install`.

`pearl dashboard` opens a live terminal view of running, queued, and
`waiting_input` jobs from every workspace. Type any Pearl command at the prompt
without the `pearl` prefix, such as `cancel <job-id>` or `respond <job-id>
"answer"`. Run `jobs` to open the selectable job list inside the dashboard; it
shows the current directory by default and `jobs --all` shows every workspace.
Press `a` inside the list to toggle between them. Enter opens the selected
job, Space preloads its run, retry, or response command, and `q` returns to the
list or dashboard. Type `archive` to browse archived jobs without leaving the
dashboard. Enter `autonomous "goal"` to start an autonomous session, or enter
`autonomous` to reopen the latest one. The autonomous screen updates in place.
Press `q` to return to the main dashboard while the session keeps running. The
dashboard returns after other commands finish. Type
`exit` or `quit`, or press `Ctrl-C`, to close it. Set `NO_COLOR=1` to disable
colors.

When the agent needs a decision, clarification, or authorization, it can pause
the job in the `waiting_input` state. The question appears in the attached
output. Respond and resume the same job with:

```bash
pearl respond <job-id> "your answer"
```

The response is added to the saved agent transcript, so the resumed run keeps
the messages and completed tool calls from before the question. A waiting job
does not occupy the worker; other queued jobs can continue in the meantime.

## Autonomous mode

`pearl autonomous "goal"` starts a persisted coordinator for the current
directory. The coordinator cannot edit files itself. It can create queued child
jobs, wait for their agents, inspect every saved result or error, and create
follow-up jobs. It finishes the session only after it reviews the child jobs and
judges the goal complete.

The autonomous TUI lists every child job and keeps an activity feed built from
the durable job status events. It records job creation and each transition such
as `queued → running` and `running → completed`. The screen stays open after the
session finishes until you press `q`.

Detaching does not stop the coordinator. Run `pearl autonomous` to reopen the
latest session, or `pearl autonomous --resume <session-id>` to open a specific
one. If a child job asks for input, detach and answer it with:

```bash
pearl respond <job-id> "answer"
```

The coordinator resumes after that job leaves `waiting_input`.

For daemon development, run the service in the foreground:

```bash
pearl daemon run
```

### Monitor daemon CPU and RAM

The resource monitor builds and starts a daemon with its own temporary config,
database, socket, and dummy API key. It does not connect to an installed Pearl
daemon or use its jobs.

```bash
./testenv/monitor-daemon.sh --duration 60 --interval 1
```

Each run writes `samples.csv`, `summary.txt`, and `daemon.log` to a new directory
under `test-results/`. The CSV records the daemon's CPU percentage and resident
memory in KiB and MiB. Use `--duration 0` to monitor until Ctrl-C, or set a custom
result location with `--output <directory>`.

## Start automatically

Install and start a per-user launchd service on macOS or systemd user service
on Linux:

```bash
pearl daemon install
pearl daemon restart
```

The service starts at login and restarts after an unsuccessful exit. A clean
`pearl daemon stop` remains stopped until it is started again or the next login.

Remove the service with:

```bash
pearl daemon uninstall
```

An asleep or powered-off laptop cannot execute jobs. Run Pearl on an always-on
machine if wall-clock 24/7 execution is required.

## Schedules

Interval schedules persist in SQLite and enqueue normal jobs when due:

```bash
pearl schedule add --every 30m --name repository-check "inspect the repository for regressions"
pearl schedule list
pearl schedule list --all
pearl schedule remove <schedule-id>
```

A schedule captures the working directory in which it was created. Scheduled
jobs use the same single-agent queue as interactive jobs, so they cannot overlap.
`schedule list` shows schedules for the current directory and its subfolders.
Use `schedule list --all` to include every workspace and add a WORKSPACE column.

## Architecture

```text
pearl CLI
    │ HTTP over a mode-0600 Unix socket
    ▼
Pearl daemon
    ├── HTTP API and event stream
    ├── SQLite jobs, events, transcripts, tool journal, schedules
    ├── scheduler ──► durable FIFO queue
    └── one worker
           └── OpenRouter agent loop
                  └── workspace-scoped file tools
```

The important package boundaries are:

- `cli`: command parsing, daemon client, streamed output, and service lifecycle.
- `internal/daemon`: local HTTP server, scheduler, cancellation, and one worker.
- `internal/store`: SQLite queue, events, checkpoints, tool journal, and schedules.
- `openrouter_request`: context-aware model/tool loop.
- `agent_functions`: filesystem operations used by model tool calls.

The local API exposes `/v1/status`, `/v1/jobs`, job
run/event/cancel/retry/respond routes, `/v1/schedules`, and `/v1/shutdown`. It
is intentionally reachable only through the Unix socket.

## Durability and recovery

Pearl writes a job to SQLite before acknowledging submission. Agent messages are
checkpointed after completed model and tool steps. Tool results are keyed by the
provider tool-call ID, allowing a retry to reuse a result that was already
recorded.

Questions awaiting user input, their pending tool-call IDs, and later responses
are stored with the job. They survive daemon restarts and resume through the
same checkpointed transcript.

Long jobs keep two durable records: the complete transcript used by `jobs view`
and a smaller context sent back to the model. When the estimated model context
passes `context_compaction_tokens`, Pearl asks the configured model for a
continuation summary and keeps about `context_keep_tokens` of the most recent
completed work. Assistant tool calls and their results are kept as one unit, so
compaction cannot create an orphaned result or cause a completed call to run
again. If the summary request fails, Pearl uses a bounded local summary and
continues. Attach output reports each compaction as a `[context]` event.

If the process disappears while a job is running, that job becomes
`interrupted` on the next start. Pearl does not automatically replay it because
future tools may have irreversible side effects. Inspect it and use
`pearl retry <job-id>` when appropriate. A job that errors before the agent
produces any work is marked `failed`; fix the blocking problem and retry it.
Queued jobs and schedules survive a restart automatically.

## Safety settings

The generated settings use one agent and conservative bounds:

```json
{
  "max_concurrency": 1,
  "max_depth": 30,
  "max_job_seconds": 1800,
  "max_file_bytes": 4194304,
  "context_compaction_tokens": 12000,
  "context_keep_tokens": 4000,
  "approved_workspace_roots": []
}
```

Context sizes are conservative estimates because exact tokenization varies by
model. Lower the trigger for models with small context windows, or raise it for
models with larger windows. `context_keep_tokens` must be smaller than the
trigger; Pearl falls back to one third of the trigger if it is not.

When `approved_workspace_roots` is empty, jobs submitted by the same local user
may target any existing directory. For a permanently unattended installation,
set it to absolute directories Pearl is allowed to work beneath:

```json
"approved_workspace_roots": [
  "/Users/me/Documents/GitHub"
]
```

File tools reject absolute paths from the model, parent-directory escapes,
`.git`, `.env*`, and symlink escapes. Reads and writes are size-limited, and
whole-file replacements use an atomic temporary-file rename.

File tools also reject symlinks and Windows junctions below the workspace,
including aliases to protected files. Approved workspace roots are compared
against the actual resolved directory.

### Agent editing and verification

Agents can create nested directories with `create_directory` and make targeted
edits with `apply_patch`. A patch replaces one exact occurrence of `old_text`;
missing or ambiguous matches require the agent to read the file again.

`list_files` and `search_files` return pages with `next_offset` and `truncated`.
They exclude common dependency/build directories and protected files. Simple
glob patterns from `.gitignore` and `.pearlignore` in the requested directory
are honored: `*.log`, `generated/`, and `docs/draft.txt`, for example. Negation,
recursive `**` patterns, and nested ignore files are not full Git-compatible
ignore processing. Search is literal and case-sensitive, with line numbers and
bounded snippets; binary and oversized files are skipped.

To let agents run tests or builds, explicitly enable trusted executables in
Pearl's durable user `settings.json`:

```json
"allowed_commands": ["go", "node"]
```

The `run_command` tool takes an executable, an argument array, and an optional
workspace-relative directory. It runs without a shell, with a default 60-second
timeout (maximum 120 seconds), 64 KiB of combined output, and child-process
cleanup. Results include the exit code, timeout state, and output truncation.
Common credential environment variables are removed from the child environment.
Commands are disabled when `allowed_commands` is absent or empty. Repository
settings cannot grant this permission; only durable user settings are consulted.

Enable this only for trusted projects: test/build programs execute repository
code with your user permissions. The working-directory check is not an OS
filesystem or network sandbox, and enabled programs can access resources outside
the workspace. Use a separate container or restricted account for untrusted code.

Incomplete model streams and token-limit responses fail the job instead of
marking a partial answer completed. Streamed output remains in job events;
retry resumes from the last complete checkpoint.

### Scripting and command help

Use an explicit path without opening a folder picker:

```bash
pearl job --workspace /path/to/project -n "fix tests" "fix failing tests"
pearl --workspace /path/to/project jobs --json
pearl jobs view "fix tests" --json
pearl status --json
pearl schedule list --all --json
pearl daemon --help
```

The global `--workspace <path>` goes before the command. `job`, `schedule add`,
and new `autonomous` goals also accept their own `--workspace` flag. JSON output
is available for `version`, `status`, `daemon status`, `jobs`, `jobs view`,
`archive`, and `schedule list`. It bypasses interactive views and emits one JSON
value on stdout, or a JSON error on stderr with a nonzero exit code. JSON queries
do not start the daemon automatically; start it with `pearl daemon start` first.

Pull requests and main-branch pushes run vet, race-enabled tests, builds, and
isolated CLI smoke tests on Linux, macOS, and Windows. Releases wait for those
checks before building and uploading binaries.
