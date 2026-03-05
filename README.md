# Orc

Orchestrate a fleet of Claude Code agents from your terminal.

```
 ██████╗ ██████╗  ██████╗
██╔═══██╗██╔══██╗██╔════╝
██║   ██║██████╔╝██║
██║   ██║██╔══██╗██║
╚██████╔╝██║  ██║╚██████╗
 ╚═════╝ ╚═╝  ╚═╝ ╚═════╝
```

Orc is a terminal app that manages a fleet of AI coding agent processes — bringing them up and pulling them down as needed. Tasks can run on a schedule or be added ad-hoc. Orc runs in the foreground with a live TUI dashboard, while other `orc` processes can add, list, and remove tasks via file-based IPC.

> [!NOTE]
> Built using AI. This is an experimental tool. Use at your own risk.

> [!WARNING]
> Orc runs agents autonomously. Depending on your configured `command`, agents may execute commands, write files, and make changes **without asking for confirmation**. Only run orc in environments where you are comfortable with fully autonomous agent operation.

## Quick Start

```bash
# Install
go install github.com/leighmcculloch/orc@latest

# Add a task
cd your-project
orc add "Refactor the auth module to use JWT tokens"

# Start the orchestrator
orc run
```

That's it. Orc picks up pending tasks and starts Claude Code agents automatically.

## Why Orc?

Running Claude Code for a single task is easy. But when you have many tasks — code reviews, refactors, migrations, daily maintenance — managing them by hand becomes tedious. You end up juggling terminal tabs, forgetting what's running, and losing track of what got done.

Orc solves this by giving you:

- **A task queue** — add tasks ad-hoc or on a schedule, orc runs them when slots are available
- **Concurrency control** — run up to N Claude Code agents in parallel
- **A live dashboard** — see what's running, what's pending, and what's done
- **Reports** — every completed task produces a summary, catalogued by date
- **Logs** — full output capture per task, viewable anytime or streamed live
- **Isolated workdirs** — each agent runs in its own directory under `.orc/workdirs/`

## How It Works

Orc runs as a **foreground process** with a TUI. It is not a daemon. All CLI commands (`orc add`, `orc list`, etc.) read and write the shared job files directly — no IPC protocol needed.

```
┌──────────────────────┐       ┌──────────────────┐
│   orc run (TUI)      │       │  orc add "task"   │
│                      │       │                    │
│  orchestrator loop   │  .orc/jobs/*.json          │
│  reads job files     │◄─────►│  writes job files  │
│  dispatches tasks    │       └────────────────────┘
│  manages agents      │
└──────────────────────┘
         │
         ├── command "task 1"
         ├── command "task 2"
         └── command "task 3"
```

Tasks queued while orc is stopped will be picked up the next time `orc run` starts.

## Commands

**Orchestrator:**

| Command | Description |
|---------|-------------|
| `orc run` | Start the orchestrator with live TUI dashboard |
| `orc log` | View today's logs |

**Tasks:**

| Command | Description |
|---------|-------------|
| `orc add <prompt>` | Add a task |
| `orc ls` | List all tasks |
| `orc rm <id>` | Remove a task |
| `orc report` | View completed task reports |

### Adding Tasks

```bash
# Simple ad-hoc task
orc add "Write unit tests for the user service"

# Scheduled task
orc add -s "daily 09:00" "Review open pull requests and summarize"

# Scheduled task
orc add -s "every 1h" "Check for API errors in the logs"
```

### Viewing Logs

```bash
# Today's orchestrator log
orc log

# Specific date
orc log -d 2025-03-15

# Stream live (like tail -f)
orc log -f

# Specific task's log
orc log -t abc123de
```

### Viewing Reports

```bash
# Today's completed tasks
orc report today

# Yesterday's completed tasks
orc report yesterday

# Specific date
orc report 2025-03-15
```

## Installation

### Go

```bash
go install github.com/leighmcculloch/orc@latest
```

### From Source

```bash
git clone https://github.com/leighmcculloch/orc.git
cd orc
go build -o orc .
```

### Prerequisites

- **Go 1.24+**: To build/install orc
- **An AI coding agent CLI**: Configure via `command` in `.orc/config.jsonc` (see [Configuration](#configuration))

## Configuration

Orc stores all configuration and state in a `.orc/` directory in the current working directory.

### Configuration File

`.orc/config.jsonc`:

```json
{
  "defaults": {
    "max_concurrent": 3,
    "command": "claude -p \"$prompt\" --dangerously-skip-permissions"
  }
}
```

### Configuration Fields

| Field | Description | Default |
|-------|-------------|---------|
| `defaults.command` | Shell command to run for each task. `$prompt` is replaced with the task prompt. **Required.** | *(none)* |
| `defaults.max_concurrent` | Max agents running in parallel | `3` |

The `command` is run via `sh -c` with `$prompt` replaced by the task prompt. Each agent runs in its own isolated directory at `.orc/workdirs/<task-id>/`.

### Command Examples

Using Claude Code directly:
```json
"command": "claude -p \"$prompt\" --dangerously-skip-permissions"
```

Using [silo](https://github.com/leighmcculloch/silo) for container isolation with Claude Code:
```json
"command": "silo claude -v -- -p \"$prompt\" --dangerously-skip-permissions"
```

Using silo with GitHub Copilot:
```json
"command": "silo copilot -v -- --model claude-opus-4.6 --allow-all-tools -p \"$prompt\""
```

## .orc/ Directory Layout

```
.orc/
├── config.jsonc                 Configuration file
├── jobs/
│   ├── meta.json               Next task ID counter
│   ├── todo.json               Pending + running tasks
│   ├── scheduled.json          Scheduled tasks
│   ├── completed.json          Completed tasks
│   └── failed.json             Failed + cancelled tasks
├── orc.pid                     PID file (exists while orc is running)
├── logs/
│   ├── orc-2025-03-15.log     Daily orchestrator log
│   └── task-abc123de.log      Per-task log
├── workdirs/
│   └── <task-id>/
│       ├── status.json         Live process status
│       ├── output.log          Full agent stdout/stderr
│       └── prompt.txt          Prompt sent to the agent
└── reports/
    └── 2025-03-15.json         Daily catalogue of completed tasks
```

### Job Files

Tasks are stored in `.orc/jobs/` across separate files by status:

| File | Contents |
|------|----------|
| `todo.json` | Pending and running tasks |
| `scheduled.json` | Tasks with a schedule |
| `completed.json` | Successfully completed tasks |
| `failed.json` | Failed and cancelled tasks |
| `meta.json` | Next task ID counter |

All files are written atomically (write to tmp file, then rename) to prevent corruption. The orchestrator holds a mutex to serialize concurrent updates from multiple agent goroutines.

### Work Directories

Each task gets its own directory at `.orc/workdirs/<task-id>/` containing:

| File | Description |
|------|-------------|
| `status.json` | Live status updated by the agent runner (starting, running, completed, failed) |
| `output.log` | Full captured stdout/stderr from the agent |
| `prompt.txt` | The prompt sent to the agent |

### Daily Reports

When a task completes, it's recorded in `.orc/reports/YYYY-MM-DD.json`:

```json
{
  "date": "2025-03-15",
  "entries": [
    {
      "task_id": "1",
      "prompt": "Refactor auth module to use JWT",
      "status": "completed",
      "finished_at": "2025-03-15T14:30:00Z"
    }
  ]
}
```

## Task Lifecycle

```
                ┌─────────┐
                │ pending  │◄─── orc add / scheduled reset
                └────┬────┘
                     │ slot available
                     ▼
              ┌──────────────┐
              │   running    │ command
              └──┬───────┬───┘
                 │       │
          success│       │error/exit!=0
                 ▼       ▼
          ┌──────────┐ ┌────────┐
          │completed │ │ failed │
          └──────────┘ └────────┘
                 │       │
                 └───┬───┘
                     ▼
              recorded in daily report
```

1. **Created** — task added via `orc add` or by a subtask writing to `jobs/inbox/` (status: `pending`)
2. **Dispatched** — orchestrator picks it up when a concurrency slot opens (status: `running`)
3. **Agent runs** — `command` executed via `sh -c`, output streamed to log
4. **Completed or Failed** — exit code checked
6. **Catalogued** — entry added to daily report file
7. **Re-scheduled** — for scheduled tasks, status resets to `pending` when the next run time arrives

### Task Statuses

| Status | Meaning |
|--------|---------|
| `pending` | Waiting to be picked up |
| `running` | Agent is executing |
| `completed` | Finished successfully |
| `failed` | Exited with non-zero code |
| `cancelled` | Interrupted by orchestrator shutdown |

## Scheduling

Tasks can be scheduled to run repeatedly using simple schedule expressions:

| Format | Example | Description |
|--------|---------|-------------|
| `every <duration>` | `every 5m`, `every 1h`, `every 30s` | Run at a fixed interval (Go duration syntax) |
| `daily HH:MM` | `daily 09:00`, `daily 17:30` | Run once per day at a specific time |
| `hourly` | `hourly` | Run every hour on the hour |

```bash
# Run every 30 minutes
orc add -s "every 30m" "Check CI pipeline status"

# Run daily at 9am
orc add -s "daily 09:00" "Review and summarize yesterday's commits"

# Run every hour
orc add -s "hourly" "Run integration test suite"
```

After a scheduled task completes (or fails), it is automatically reset to `pending` when the next scheduled time arrives.

## TUI Dashboard

When you run `orc run`, you get a live terminal dashboard showing:

- **Running** — currently executing tasks with elapsed time
- **Pending** — tasks waiting for a slot, with schedule info
- **Completed Recently** — last 10 completed tasks with time since completion
- **Failed** — recent failures with error messages
- **Activity Log** — real-time event stream (task added/started/completed/failed/removed)

Press `q` or `Ctrl+C` to shut down the orchestrator.

## Examples

### Minimal Usage

```bash
orc add "Fix the broken tests in the auth module"
orc run
```

### Daily Automation

```bash
# Morning code review
orc add -s "daily 09:00" "Review open PRs and write summary comments"

# Hourly health check
orc add -s "every 1h" "Check application logs for errors and report anomalies"

# End-of-day summary
orc add -s "daily 17:00" "Summarize today's git commits across all branches"

# Start and leave running
orc run
```

### Reviewing What Got Done

```bash
# What did the agents accomplish today?
orc report today

# What about yesterday?
orc report yesterday

# Check a specific date
orc report 2025-03-10

# View detailed log for a specific task
orc log -t abc123de
```

### Queuing Tasks While Orc Runs

In one terminal:
```bash
orc run
```

In another terminal:
```bash
# These write directly to the shared job files
orc add "Migrate the user table to add email verification column"
orc add "Write API documentation for the payments endpoint"
orc ls
```

### Queuing Tasks Before Starting Orc

```bash
# Queue up work
orc add "Task one"
orc add "Task two"
orc add "Task three"

# Start later — all three tasks will be picked up
orc run
```

## License

Copyright 2026 Stellar Development Foundation (This is not an official project of the Stellar Development Foundation)

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
