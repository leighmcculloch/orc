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

Orc is a terminal app that manages a fleet of Claude Code processes — bringing them up and pulling them down as needed. Tasks can run on a schedule or be added ad-hoc. Orc runs in the foreground with a live TUI dashboard, while other `orc` processes can add, list, and remove tasks via file-based IPC.

> [!NOTE]
> Built using AI. This is an experimental tool. Use at your own risk.

## Quick Start

```bash
# Install
go install github.com/leighmcculloch/orc@latest

# Initialize
cd your-project
orc init

# Add a task
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
- **Environments** — named configs with working directories and pre-run hooks

## How It Works

Orc runs as a **foreground process** with a TUI. It is not a daemon. While it's running, other `orc` processes communicate with it by writing JSON files to an inbox directory. The running orchestrator polls the inbox, processes commands, and writes responses to an outbox directory.

```
┌──────────────────────┐       ┌──────────────────┐
│   orc run (TUI)      │       │  orc add "task"   │
│                      │ .orc/ │                    │
│  orchestrator loop   │◄──────│  writes to inbox/  │
│  polls inbox/        │       │  polls outbox/     │
│  dispatches tasks    │──────►│  reads response    │
│  manages agents      │       └──────────────────  ┘
└──────────────────────┘
         │
         ├── claude --print "task 1"
         ├── claude --print "task 2"
         └── claude --print "task 3"
```

When orc is not running, commands like `orc add` and `orc list` fall back to reading and writing the state file directly. Tasks queued while orc is stopped will be picked up the next time `orc run` starts.

## Commands

| Command | Description |
|---------|-------------|
| `orc run` | Start the orchestrator with live TUI dashboard |
| `orc add <prompt>` | Add an ad-hoc task |
| `orc list` | List all tasks |
| `orc remove <id>` | Remove a task |
| `orc status` | Show orchestrator status (running/pending/completed counts) |
| `orc log` | View today's logs |
| `orc report` | View today's completed task reports |
| `orc init` | Initialize `.orc/` directory with default config |
| `orc stop` | Stop the running orchestrator |
| `orc help` | Show usage information |

### Adding Tasks

```bash
# Simple ad-hoc task
orc add "Write unit tests for the user service"

# Task with a specific environment
orc add -e myproject "Run the linter and fix all warnings"

# Scheduled task
orc add -s "daily 09:00" "Review open pull requests and summarize"

# Scheduled task with environment
orc add -e backend -s "every 1h" "Check for API errors in the logs"
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
- **Claude Code CLI**: The `claude` command must be available in `$PATH` (or configured via `claude_code_path` in config)

## Configuration

Orc stores all configuration and state in a `.orc/` directory in the current working directory. Run `orc init` to create it with defaults.

### Configuration File

`.orc/config.json`:

```json
{
  "environments": {
    "default": {
      "name": "default",
      "work_dir": ".",
      "pre_hooks": []
    }
  },
  "defaults": {
    "environment": "default",
    "max_concurrent": 3,
    "claude_code_path": "claude"
  }
}
```

### Configuration Fields

| Field | Description | Default |
|-------|-------------|---------|
| `defaults.environment` | Default environment for new tasks | `"default"` |
| `defaults.max_concurrent` | Max Claude Code agents running in parallel | `3` |
| `defaults.claude_code_path` | Path to the Claude Code CLI binary | `"claude"` |

### Environments

Environments let you define named configurations for different projects or contexts. Each environment has a working directory and optional pre-run hooks.

| Field | Description |
|-------|-------------|
| `name` | Environment name (must match the key) |
| `work_dir` | Working directory for Claude Code (where it runs) |
| `pre_hooks` | Shell commands to run before Claude Code starts |

```json
{
  "environments": {
    "default": {
      "name": "default",
      "work_dir": ".",
      "pre_hooks": []
    },
    "backend": {
      "name": "backend",
      "work_dir": "/home/user/projects/backend",
      "pre_hooks": [
        "git pull origin main",
        "npm install"
      ]
    },
    "infra": {
      "name": "infra",
      "work_dir": "/home/user/projects/infrastructure",
      "pre_hooks": [
        "terraform init"
      ]
    }
  }
}
```

### Pre-run Hooks

Pre-hooks run sequentially in the environment's `work_dir` before Claude Code starts. If any hook fails, the task fails without starting Claude Code.

Use hooks for:
- Pulling latest changes (`git pull`)
- Installing dependencies (`npm install`, `go mod tidy`)
- Setting up the environment (`source .env`, `terraform init`)
- Running pre-flight checks

## .orc/ Directory Layout

```
.orc/
├── config.json                 Configuration file
├── state.json                  Task list and statuses
├── orc.pid                     PID file (exists while orc is running)
├── inbox/                      IPC: incoming command files
├── outbox/                     IPC: response files
├── logs/
│   ├── orc-2025-03-15.log     Daily orchestrator log
│   └── task-abc123de.log      Per-task log
├── workdirs/
│   └── abc123de/
│       ├── status.json         Live process status
│       ├── output.log          Full Claude Code stdout/stderr
│       └── report.md           Summary report (written by Claude)
└── reports/
    └── 2025-03-15.json         Daily catalogue of completed tasks
```

### State File

`.orc/state.json` tracks all tasks. It is written atomically (write to tmp file, then rename) to prevent corruption. The orchestrator holds a mutex to serialize concurrent updates from multiple agent goroutines.

### Work Directories

Each task gets its own directory at `.orc/workdirs/<task-id>/` containing:

| File | Description |
|------|-------------|
| `status.json` | Live status updated by the Claude Code runner (starting, running_hook, running, completed, failed) |
| `output.log` | Full captured stdout/stderr from Claude Code |
| `report.md` | Summary report — Claude Code is instructed to write this on completion |

### Daily Reports

When a task completes, it's recorded in `.orc/reports/YYYY-MM-DD.json`:

```json
{
  "date": "2025-03-15",
  "entries": [
    {
      "task_id": "abc123de",
      "prompt": "Refactor auth module to use JWT",
      "report": "Refactored the auth module to use JWT tokens...",
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
              │   running    │ pre-hooks → claude --print
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

1. **Created** — task added via `orc add` or file-based IPC (status: `pending`)
2. **Dispatched** — orchestrator picks it up when a concurrency slot opens (status: `running`)
3. **Pre-hooks** — environment's `pre_hooks` execute in the `work_dir`
4. **Claude Code runs** — `claude --print --output-format text "<prompt>"`, output streamed to log
5. **Completed or Failed** — exit code checked, report.md read if present
6. **Catalogued** — entry added to daily report file
7. **Re-scheduled** — for scheduled tasks, status resets to `pending` when the next run time arrives

### Task Statuses

| Status | Meaning |
|--------|---------|
| `pending` | Waiting to be picked up |
| `running` | Claude Code is executing |
| `completed` | Finished successfully |
| `failed` | Exited with non-zero code or hook failure |
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

## Inter-Process Communication

Orc uses **file-based IPC** — no sockets, no network. This is how a second `orc` process (e.g., `orc add`) talks to the running orchestrator:

1. CLI writes a command JSON file to `.orc/inbox/<request-id>.json`
2. Orchestrator polls inbox every second, picks up the file, deletes it
3. Orchestrator processes the command and writes a response to `.orc/outbox/<request-id>.json`
4. CLI polls outbox (100ms interval, 30s timeout), reads the response, deletes it

All writes are atomic (write to `.tmp`, then rename) to prevent partial reads.

### Supported IPC Commands

| Command | Description |
|---------|-------------|
| `add_task` | Add a new task to the queue |
| `list_tasks` | Get all tasks |
| `remove_task` | Remove a task by ID |
| `get_status` | Get running/pending/completed/failed counts |
| `stop` | Shut down the orchestrator |

## Examples

### Minimal Usage

```bash
orc init
orc add "Fix the broken tests in the auth module"
orc run
```

### Multi-Environment Setup

```json
{
  "environments": {
    "default": {
      "name": "default",
      "work_dir": ".",
      "pre_hooks": []
    },
    "frontend": {
      "name": "frontend",
      "work_dir": "/home/user/app/frontend",
      "pre_hooks": ["npm install"]
    },
    "backend": {
      "name": "backend",
      "work_dir": "/home/user/app/backend",
      "pre_hooks": ["go mod tidy"]
    }
  },
  "defaults": {
    "environment": "default",
    "max_concurrent": 2,
    "claude_code_path": "claude"
  }
}
```

```bash
orc add -e frontend "Add dark mode toggle to the settings page"
orc add -e backend "Add rate limiting middleware to the API"
orc add -e frontend "Write Cypress tests for the login flow"
orc run
```

### Daily Automation

```bash
# Morning code review
orc add -s "daily 09:00" -e backend "Review open PRs and write summary comments"

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
# These go through file-based IPC to the running orchestrator
orc add "Migrate the user table to add email verification column"
orc add "Write API documentation for the payments endpoint"
orc list
orc status
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
