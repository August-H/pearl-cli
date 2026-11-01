# Pearl CLI

Pearl is an agent task manager. You can assign work as jobs, close the terminal, return later, and get information on the agent changes. Use an eligible ChatGPT subscription or an OpenRouter API key.
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
prepares your PATH. No compiler or repository clone needed. GPT models also need
the [Codex CLI](https://github.com/openai/codex), which you install yourself.

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

Link your ChatGPT account to use your plan in Pearl:

```bash
pearl auth login chatgpt
pearl model
```

Complete sign-in and approve ChatGPT plan usage in your browser. Pearl selects
a model from your account's catalog and switches to ChatGPT automatically.
Eligible Plus and Pro accounts can use the allowance their plan makes available
to connected apps. Account eligibility and usage limits are controlled by OpenAI.

Or run the setup wizard and enter your OpenRouter API key:

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
pearl job "inspect this repository and fix the failing tests" -n "fix tests"
pearl run "fix tests"
pearl jobs
```

For a larger goal, start autonomous mode from the project directory:

```bash
pearl autonomous "audit the release, fix the problems you find, and verify the result"
```

Pearl opens a live TUI and keeps the coordinator in the daemon. Press Esc to
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

### ChatGPT accounts

```bash
pearl auth login chatgpt                 # Continue with ChatGPT in your browser
pearl auth login chatgpt --no-browser    # Print the URL without opening a browser
pearl auth status                        # Show the selected account and provider
pearl auth accounts                      # List saved registrations and their IDs
pearl auth login chatgpt --new-account   # Add another account or workspace
pearl auth use <id>                      # Select a connected registration
pearl auth login chatgpt --account <id>  # Reauthorize a saved registration
pearl auth logout                        # Revoke the selected session and sign out
pearl provider openrouter                # Switch to your saved OpenRouter model
pearl provider chatgpt                   # Switch back to your connected account
```

The model you select decides the provider. The daemon
reads the selection when a job starts; no restart is required. A running job
keeps its original ChatGPT account. Provider changes apply to jobs that start
afterward, including retries. Each provider remembers its own model.
`pearl configure` selects OpenRouter again.

`pearl model` and `pearl model --list` show the models from every account you're
signed in to: Claude from Claude Code, GPT from ChatGPT, and Gemini from
Antigravity. Each one reports its own current list. Selecting a model, in the menu
or with `pearl model --set <id>`, also switches to its provider and harness. The
list tells you which accounts aren't signed in yet.

Pearl uses OpenAI's documented [Sign in with ChatGPT public-client flow](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)
and [Responses API route for plan usage](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference).
It needs no proxy installation or API key. Sign-in uses a temporary callback on
`127.0.0.1`; `--no-browser` still requires the browser to reach that callback.
For SSH, forward the port shown in the URL's `redirect_uri` parameter, or sign
in on the machine running Pearl. The attempt expires after five minutes.

Credentials are kept in `chatgpt.json` in Pearl's user config directory, with
owner-only file permissions on Unix. Pearl verifies the sign-in identity,
refreshes tokens automatically, and keeps its credentials separate from Codex
and other apps. Logout clears the selected account's tokens while retaining its
registration for later sign-in. If revocation cannot be confirmed, Pearl reports
that and still clears local tokens.

ChatGPT plan limits apply to these requests. Manage the app's allowance and
access in [ChatGPT usage settings](https://chatgpt.com/#settings/Usage).
When a request reaches a limit, Pearl fails the job with an explanation. It does
not switch billing providers automatically. The current OpenAI preview supports
Pearl's local function tools; hosted tools such as image generation, Code
Interpreter, connectors, and tool search are unavailable on this route. See
[OpenAI's preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).

Native ChatGPT runs retry interrupted connections up to twice before any output
has been displayed. If ChatGPT rejects saved response state, Pearl rebuilds the
conversation once from saved messages and tool results, then saves the repaired
history. Completed tool calls remain recorded. Authentication and usage-limit
errors still require action; Pearl does not retry those automatically.

`configure` asks for the OpenRouter API key and then offers the free router or
a custom model ID. It stores the selection in the durable `settings.json`. The
same wizard runs inside the dashboard command box when you enter `configure`.

OpenRouter's catalog appears only when OpenRouter is selected and you aren't signed
in to any of those accounts. Then `pearl model` opens a lab menu for it, with batch
variants, GPT Pro models, and media generation models hidden. The menu keeps
text chat models, including those that accept images, and hides models that
produce images, video, or audio. Only models added to OpenRouter within the past
calendar year appear. Models with missing dates or future dates are hidden,
and the Free Tier router remains available. Select a lab with Enter, then select one of
its models with Enter to save it. Starred
labs appear first in this order: Anthropic, OpenAI, Google, Grok, DeepSeek,
Meta, Kimi, and GLM. Use the
arrow keys, `j`/`k`, the mouse wheel or trackpad to navigate, and type to search
by model name. Searching from the labs menu lists matching models from every
lab. Long wheel swipes accelerate in both the model picker and
jobs lists; short ticks move one row at a time. Left arrow returns to the labs.
Esc clears a search, then goes back or closes the lab menu; Ctrl-C closes the
picker from either menu. The Pearl lab contains the Free Tier option, `openrouter/free`.
Inside a lab, each row shows the model's readable name, sorted newest first by
the date it was added to OpenRouter. Enter `model` in the
dashboard to open the same picker.

Your signed-in accounts' models open right away from a saved list. If that list is
over 5 minutes old, Pearl checks for changes in the background, and they appear the
next time you open the picker. Signing in or out through Pearl clears the list.
Add `--refresh` to wait for an up-to-date list. OpenRouter's catalog is fetched
each time, with a cached list available if that fails.

Use `--list` to print the catalog, `--set <model-id>` to set a model directly, or `--free` to use the
Free Tier.

## Dashboard

`pearl dashboard` opens a live terminal view of running, queued, and
`waiting_input` jobs in the current workspace, with recent finished jobs below.
Use `pearl --workspace <path>` to open the dashboard for another directory.
Type any Pearl command at the prompt
without the `pearl` prefix, such as `cancel <job-id>` or `respond <job-id>
"answer"`. Commands that take a job ID, such as `run`, `attach`, `respond`,
`cancel`, `retry`, and `jobs view`, list matching jobs as you type; press Tab
to fill one in. Run `jobs` to open the selectable job list inside the dashboard; it
shows the current directory by default and `jobs --all` shows every workspace.
Press `a` inside the list to toggle between them. Enter opens the selected
job, Space preloads its run, retry, or response command, and Esc returns to the
list or dashboard. Type `archive` to browse archived jobs without leaving the
dashboard. Enter `autonomous "goal"` to start an autonomous session, or enter
`autonomous` to reopen the latest one. The autonomous screen updates in place.
Press Esc to return to the main dashboard while the session keeps running. The
dashboard returns after other commands finish. Type
`exit` or `quit`, or press `Ctrl-C`, to close it. Set `NO_COLOR=1` to disable
colors. Job suggestions also use the current workspace. Open `jobs --all` when
you need a job from another directory.

The composer stays at the bottom of the screen. When the output above it doesn't
fit, scroll with the mouse wheel or PgUp/PgDn, and press Home or End to jump to the
top or bottom. A line above the composer shows how much is out of view.

The composer wraps long drafts and keeps pasted newlines without submitting
them. Press Shift+Enter to insert a newline, or Ctrl+E to edit the entire draft in
`$VISUAL`, `$EDITOR`, or `vi`. Enter submits the draft. Some terminals, including
macOS Terminal, can't tell Shift+Enter from Enter, so Shift+Enter submits there. Use
Ctrl+J instead, which works everywhere. Alt+Enter also adds a newline. Quote multiline prompts
as you would any argument, for example `respond <job-id> "first line` followed
by the remaining lines and a closing quote. A command error keeps the draft.

Each running job shows its recorded provider, model, harness, and test status.
Each job shows how it was billed: ChatGPT subscription, Claude subscription, Gemini
subscription, or API.
The composer shows the selected model and its harness; changing the model
does not relabel a run already in progress. `jobs view <job-id>` includes these
details and the test command. Tests start as `not run`, become `running` when a
recognized test command starts, then `passed` or `failed` from its exit status.
A later code edit clears an earlier result. Documentation edits keep the result.
These states describe recognized test commands, not test coverage. Complex shell
scripts whose test exit status cannot be established stay unverified.

## Jobs and directories

Create a job from the directory Pearl should work in, then run it by ID:

```bash
pearl job "inspect this repository and fix the tests" -n "fix tests"
pearl run "fix tests"
```

`pearl job` saves the prompt with a `pending` status and does not run it.
`pearl run <job-id>` queues a pending job or retries a finished job, starts the
background daemon when needed, and streams the result. Add `--detach` before
the ID to run it in the background.

Without `-n`, Pearl gives the job a random two-word ID such as `jaunty-narwhal`.
Add `-n "name"` after the prompt to use that name as the job ID instead. Custom
IDs can contain up to 20 characters and cannot begin with a hyphen.

Add `--directory` or `-d` after the prompt to choose another workspace with a
native folder picker. Pearl validates the selection before saving the job. macOS uses the
system folder dialog. Windows uses its PowerShell folder browser. Linux uses
Zenity or KDialog when either is installed.

```bash
pearl job "update the documentation" -n "fix docs" --directory
```

### Paused jobs and follow-ups

When the agent needs a decision, clarification, or authorization, it can pause
the job in the `waiting_input` state. The question appears in the attached
output. Respond and resume the same job with:

```bash
pearl respond <job-id> "your answer"
```

The response is added to the saved agent transcript, so the resumed run keeps
the messages and completed tool calls from before the question. A waiting job
does not occupy the worker; other queued jobs can continue in the meantime.

`pearl respond` also works on completed, failed, interrupted, and cancelled
jobs. The message is queued as a follow-up: Pearl jobs continue the saved
conversation, and Codex jobs resume their Codex session.

## Folders

A folder groups jobs that work in one directory. Create it once, then add jobs
to it by name instead of picking a directory each time:

```bash
pearl folder create site                      # uses this directory
pearl folder create site --workspace ~/code/site
pearl job "fix the broken links" --folder site
pearl folder                                  # list folders and job counts
pearl folder edit site                        # pick a new directory
pearl folder remove site                      # its jobs stay on the board
```

In `pearl jobs` and the dashboard's job list, folders appear above the other
jobs. Press Enter to open one and Esc to go back. Space or `n` starts a new job
in it, `e` opens the directory picker to move it, and `f` starts a new folder.

Moving a folder moves its jobs too, so pending and finished jobs run in the new
directory next time. Running jobs and jobs waiting for an answer keep the
directory they started in.

## Skills

A skill is a Markdown file of instructions for one kind of task, such as how
you like commits written or how to ship a release. Every job can use your
skills, with any harness.

```bash
pearl skills          # open the skills folder and list your skills
pearl skills list     # list them without opening the folder
pearl skills path     # print the folder path
```

Save each skill as `<name>.md` or `<name>/SKILL.md` in that folder. Front
matter is optional:

```markdown
---
name: ship-release
description: Tag, build, and publish a release
---
1. Run the tests...
```

Without front matter, the file or folder name is the skill name and the first
line of text is its description. Jobs see each skill's name and description
and load the full text only when a skill fits the task. Changes apply to the
next job that starts.

## Usage limits

```bash
pearl usage                       # the plan for the selected model
pearl usage chatgpt               # or name one: chatgpt, claude, antigravity
```

`pearl usage` shows how much of each limit you've used and when it resets. It asks
each provider directly, so it's live and uses no tokens. It's also a dashboard
command.

- **Claude** asks your installed Claude Code.
- **ChatGPT** asks Codex, because Pearl's own ChatGPT sign-in can't read limits. Codex
  must be signed in too, and Pearl warns you if that's a different account.
- **Gemini** uses Pearl's Antigravity sign-in.

API billing has no plan limits to show. That includes OpenRouter, and Claude Code
when `ANTHROPIC_API_KEY` is set.

To see everything at once, run `pearl accounts`. It lists every account you're
signed in to with its limits and marks the one in use. It's also a dashboard
command. A ChatGPT account that Codex isn't signed in to is listed without limits,
because OpenAI only gives them to Codex's own login.

## Harnesses

A harness is the agent loop that does the work. You don't choose one: the model
you pick in `pearl model` decides it.

| Model | Sign-in | Harness |
|---|---|---|
| Claude | `pearl auth login claude` | Your installed Claude Code |
| GPT | `pearl auth login chatgpt` | Your installed Codex |
| Gemini | `pearl auth login antigravity` | Your installed Antigravity server |
| OpenRouter | `pearl configure` | Pearl's own agent loop |

A new job or a retry uses the model selected when it starts. Answering a question
or following up continues in the harness that holds the conversation, if it can
still run. `pearl jobs view` shows which harness ran each job.

Pearl doesn't include or download any of these tools. GPT models run on the Codex
CLI you install yourself (`npm install -g @openai/codex`), and Codex's updates are
yours to manage too. If you used an earlier version of Pearl, its downloaded copy of
Codex is deleted the next time the daemon starts.

```bash
pearl codex                  # show which Codex the harness uses
```

Pearl looks for `codex` on your `PATH`, then in common install locations. Set
`PEARL_CODEX_PATH` to use a specific Codex instead.

Codex jobs run in Codex's `workspace-write` sandbox: commands can change files
in the job directory but have no network access. Pearl ignores your
`~/.codex/config.toml` for these runs so Pearl's provider settings apply. With
ChatGPT, an authenticated loopback transport refreshes tokens for Codex without
passing OAuth credentials to the child process. A
retried Codex job resumes its Codex session.

Pearl tells Codex to start its final message with `PEARL_QUESTION:` when it
needs something from you. The job then pauses like any other job that is
waiting for input, and `pearl respond <job-id> "answer"` sends your answer back
into the same Codex session. Weaker models sometimes ask in plain text instead,
in which case the job finishes with the question as its result. Codex also sends a
much larger prompt with each request than Pearl's harness, which costs more on
paid models. Autonomous mode always uses Pearl's harness for its own planning.

### Claude Code

```bash
pearl auth login claude           # sign in with Claude Code's own login
pearl auth logout claude          # sign out of Claude Code
pearl claude                      # show which Claude Code Pearl uses
```

Pearl runs the `claude` CLI you installed, and it uses that CLI's sign-in and
plan. Pearl never installs or bundles Claude Code. It looks for `claude` on your
`PATH` and in common install locations. Set `PEARL_CLAUDE_PATH` to use a specific
one.

Bash commands run in Claude Code's own sandbox, which can write only to the
workspace and temp folders and can't read the paths Pearl protects. Pearl answers
Claude Code's other permission requests itself:

- Files outside the workspace can be read, except protected paths. Only files
  inside the workspace can be changed.
- `WebFetch` and network access from commands need `allow_command_network`.
- Commands that can't be sandboxed need `allow_unsandboxed_commands`.
- MCP tools and any tool Pearl doesn't recognize are refused.

Pearl always uses your Claude sign-in, so Anthropic models in `pearl model` really
run on Claude. It ignores shell variables that send Claude Code to another endpoint
or rename its models, such as `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` and
`ANTHROPIC_DEFAULT_OPUS_MODEL`. Running `claude` yourself still uses them.

Your own Claude Code settings, hooks, and `CLAUDE.md` files still apply. Claude
Code may leave a `.claude` folder in the workspace. Retrying or answering a job
resumes its Claude Code session.

### Antigravity

```bash
pearl auth login antigravity      # sign in with your Google account
pearl auth logout antigravity     # remove Pearl's saved sign-in
pearl antigravity                 # show which server Pearl uses
```

Pearl talks to an Antigravity ACP server (`agy_acp_server`) over the Agent Client
Protocol. Pearl never downloads or bundles it. `Antigravity.app` doesn't include
this server, so Pearl looks in this order: `PEARL_ANTIGRAVITY_PATH`, your `PATH`,
then the copy T3 Code installs for its Antigravity provider. It needs
`localharness_external` in the same folder.

Pearl keeps its own Google sign-in in `~/.gemini/pearl-antigravity`, apart from
T3 Code and your other tools, so run `pearl auth login antigravity` once. Set
`PEARL_ANTIGRAVITY_HOME` to move it. If a saved sign-in stops working, the job
fails and asks you to sign in again. Only Google account sign-in is supported.

The whole server runs in Pearl's command sandbox, because the server runs its own
commands. They can write only to the workspace, temp folders, per-user caches, and
that sign-in folder, and they can't read protected paths. Two limits follow from
sharing one sandbox: commands have network access, because the server needs it to
reach the model, and they can read the saved sign-in. They can also open
pseudo-terminals, which the server runs commands in. Use Antigravity only with
projects you trust. Where no sandbox is available, jobs fail unless you set
`allow_unsandboxed_commands`.

Skills are sent with the first prompt, up to 64 KiB in total. Retrying or
answering a job resumes its Antigravity session.

The durable queue, schedules, and daemon lifecycle remain available as advanced
operations:

```bash
pearl
pearl help
pearl job "fix the login flow" -n "login fix"
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
the selectable list, use the arrow keys, the mouse wheel or trackpad, or `j`
and `k` to move between jobs. Use Page Up and Page Down to move faster, and
Enter to open the selected job. Space preloads `run <job-id>` for pending
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
scroll, `a` to expand everything, `c` to collapse everything, and Esc to exit.
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

## Autonomous mode

`pearl autonomous "goal"` starts a persisted coordinator for the current
directory. Its jobs use the selected model. The coordinator itself plans with
ChatGPT or OpenRouter, so with a Claude or Google model selected you also need one
of those. The coordinator cannot edit files itself. It can create queued child
jobs, wait for their agents, inspect every saved result or error, and create
follow-up jobs. It finishes the session only after it reviews the child jobs and
judges the goal complete.

The autonomous TUI lists every child job and keeps an activity feed built from
the durable job status events. It records job creation and each transition such
as `queued → running` and `running → completed`. The screen stays open after the
session finishes until you press Esc.

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
           ├── Pearl or Codex agent loop, using ChatGPT or OpenRouter
           │      └── workspace-scoped file tools
           └── installed Claude Code CLI or Antigravity server, using its own sign-in
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

Only one daemon can run for a config folder. It holds a lock file for as long as
it runs, and the system releases that lock if it dies. A second daemon refuses to
start, and `pearl daemon restart` waits for the old one to finish.

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

Pearl writes these settings to its config directory on first run. It never reads
a `settings.json` from the repository you're working in. They use one agent and
conservative bounds:

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

Commands run in an OS sandbox: `sandbox-exec` on macOS and bubblewrap (`bwrap`)
on Linux. They can write only to the workspace, temp folders, per-user caches,
and outside paths granted to the job. They can't read Pearl's own config or the
credential folders in your home directory, but they can read most other files on
the machine. They have no network access, except that on macOS they can still
reach `localhost`, because test suites need it for their local servers. On Linux,
they get a private network with its own `localhost`.

Linux without bubblewrap and Windows have no sandbox, so commands fail there
until you opt out. These settings go in the same user `settings.json`:

```json
"allow_unsandboxed_commands": true,
"allow_command_network": true,
"allowed_command_env": ["MY_BUILD_SETTING"]
```

`allow_unsandboxed_commands` runs commands without a sandbox where none is
available, and `allow_command_network` gives sandboxed commands the network.

Commands also get a reduced environment: the variables that system, C, Go, Node,
Python, Ruby, Rust, and Java tools need, such as `PATH`, `HOME`, and `GOCACHE`.
Everything else is withheld, including credentials such as `AWS_ACCESS_KEY_ID`
and `DATABASE_URL`, and `SSH_AUTH_SOCK`. Use `allowed_command_env` to pass more
variables through.

Enable commands only for trusted projects: test and build programs execute
repository code with your user permissions. Use a separate container or
restricted account for untrusted code.

### Access outside the workspace

An agent that needs a path outside its workspace can request read or write access
to it for the current job. The `outside_access` setting decides what happens:

- `review` (the default): a separate model call with no tools approves or denies
  the request based on the job's task.
- `ask`: the job pauses until you answer. Only `y` or `yes` approves it; any other
  answer denies it.
- `deny`: every request is refused.

Pearl treats any other value as `deny`, so a typo can't weaken the setting. Paths
that hold credentials or Pearl's own files are always refused. Pearl reads this
setting only from your user `settings.json`.

Incomplete model streams and token-limit responses fail the job instead of
marking a partial answer completed. Streamed output remains in job events;
retry resumes from the last complete checkpoint.

### Scripting and command help

Use an explicit path without opening a folder picker:

```bash
pearl job "fix failing tests" -n "fix tests" --workspace /path/to/project
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
