# Orc — AI Agent Orchestrator

A terminal CLI app (Go) that orchestrates a fleet of AI coding agent processes. It runs as a long-running foreground app with a TUI. CLI commands read/write shared job files directly.

## Build & Run

```bash
cd orc
go build -o orc .
./orc run           # starts orchestrator with TUI (foreground)
```

On first run, orc automatically creates the `.orc/` directory and a default `config.jsonc`.

## Commands

```
orc run                                   Start orchestrator (foreground TUI)
orc status [--json]                       Show orchestrator status overview
orc add <prompt>                          Add ad-hoc task
orc add -s <schedule> <prompt>            Add scheduled task
orc ls                                    List all tasks
orc rm <id>                               Remove a task
orc kill [task-id]                        Kill a running task (interactive picker if no id)
orc log [-d YYYY-MM-DD] [-f] [-t <id>]   View/stream logs
       [-l level] [--output-only]
orc report [today|yesterday|YYYY-MM-DD]   View completed task reports
```

### orc status

Shows whether the orchestrator is running, task counts by status, running/pending tasks, and recent failures. Use `--json` for machine-readable output.

### orc kill

Kills a running task. If the orchestrator is running, writes a kill request file to `jobs/kill/` which the orchestrator picks up. If the orchestrator is not running, sends SIGTERM directly to the task's process group. Without a task ID argument, shows an interactive picker of running tasks.

### orc log

- `-l, --level`: Filter log lines by level — `"all"` (default), `"orc"` (orchestrator/scheduler only), or `"task"` (task events only).
- `--output-only`: Show only the agent's stdout/stderr (use with `-t`).
- `-t, --task`: View logs for a specific task. Without `--output-only`, shows interleaved orchestrator task log + agent output.
- `-f, --follow`: Stream log output in real-time.

## Architecture

```
orc/
├── main.go                              CLI entry point, all commands
├── config/config.go                     Config types, load/save, .orc dir management, pid file
├── state/state.go                       Task model, Store with mutex-protected JSON persistence
├── orchestrator/orchestrator.go         Core engine: main loop, task dispatch, inbox polling,
│                                         kill polling, retry logic, timeout enforcement,
│                                         log/workdir cleanup, desktop notifications
├── orchestrator/schedule.go             Schedule parsing
├── agent/agent.go                       Agent process lifecycle (exec, subtask support,
│                                         graceful shutdown with SIGTERM→SIGKILL)
├── logging/logging.go                   Per-day + per-task log files, streaming, level filtering
├── report/report.go                     Daily report catalogue (completed task summaries)
├── tui/tui.go                           Bubbletea TUI dashboard (live status, search, kill)
├── tui/git.go                           Git diff stat detection for notifications
├── notify/notify.go                     Desktop notifications (macOS + Linux)
└── pick/pick.go                         Interactive task picker
```

## .orc/ Directory Layout

All state and config lives in `.orc/` in the current working directory:

```
.orc/
├── config.jsonc          # defaults (max_concurrent, command, timeouts, retries, etc.)
├── jobs/
│   ├── meta.json        # next task ID counter
│   ├── todo.json        # pending + running tasks
│   ├── scheduled.json   # scheduled tasks
│   ├── completed.json   # completed tasks
│   ├── failed.json      # failed + cancelled tasks
│   ├── inbox/           # prompt files dropped by agents to create subtasks
│   └── kill/            # kill request files (task ID as filename)
├── orc.pid              # pid file, exists only while orchestrator is running
├── logs/
│   ├── orc-YYYY-MM-DD.log    # daily orchestrator log
│   └── task-<id>.log         # per-task orchestrator log
├── workdirs/
│   └── <task-id>/
│       ├── status.json        # live process status (written by agent runner)
│       ├── output.log         # full agent stdout/stderr capture
│       ├── prompt.txt         # prompt sent to the agent
│       └── command.txt        # resolved shell command (for debugging)
└── reports/
    └── YYYY-MM-DD.json        # daily catalogue of completed task entries
```

## Key Design Decisions

- **Shared job files, no IPC.** CLI commands and the orchestrator read/write the same job files directly. No inbox/outbox protocol needed.
- **pid file for liveness.** `.orc/orc.pid` is created on `orc run` and removed on shutdown. `config.IsRunning()` checks the pid file and validates the process is alive.
- **All JSON.** Config, job files, status files, reports — everything is JSON.
- **Tasks stored in separate files by status.** `jobs/todo.json`, `jobs/scheduled.json`, `jobs/completed.json`, `jobs/failed.json`. `state.Store` uses `sync.Mutex` for concurrent updates. All writes are atomic (tmp+rename).
- **Agent command is configurable.** Set `defaults.command` in config. The `$prompt` placeholder is replaced with a shell command that reads the prompt file. No default — must be configured.
- **Tasks can create subtasks.** Orc instructions are appended to every prompt telling agents to write a prompt file into `jobs/inbox/` which the orchestrator picks up.
- **Scheduled tasks stay in the task list.** After completing, the orchestrator resets them to pending when the next scheduled time arrives.
- **TUI uses bubbletea with alt screen.** Refreshes every 1s via tick, receives events from orchestrator via channel.
- **Kill via file-based IPC.** `orc kill` writes a file to `jobs/kill/<task-id>`, which the orchestrator polls and processes by cancelling the task's context.
- **Graceful agent shutdown.** On cancellation/timeout, SIGTERM is sent to the process group. After the grace period, SIGKILL is sent.
- **Retry with backoff.** Failed tasks are re-queued as pending with a `retry_after` timestamp. The orchestrator skips tasks whose backoff hasn't elapsed.
- **Log cleanup at startup.** The orchestrator removes log files, reports, and workdirs older than the retention period on startup.

## Config Format (.orc/config.jsonc)

```jsonc
{
  "defaults": {
    "max_concurrent": 3,                                              // int, default 3
    "command": "claude -p \"$prompt\" --dangerously-skip-permissions", // string, required
    "task_timeout": "30m",                                            // Go duration, optional
    "max_retries": 2,                                                 // int, optional (0 = no retries)
    "retry_backoff": "30s",                                           // Go duration, default "30s"
    "shutdown_grace": "10s",                                          // Go duration, default "10s"
    "log_retention_days": 7                                           // int, default 7
  },
  "notifications": {
    "desktop": false                                                  // bool, default false
  }
}
```

### Config Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `defaults.max_concurrent` | `int` | `3` | Maximum number of tasks running simultaneously |
| `defaults.command` | `string` | (required) | Shell command to run agents. `$prompt` is replaced with the task prompt. Run via `sh -c`. |
| `defaults.task_timeout` | `string` | (none) | Go duration (e.g. `"30m"`, `"1h"`). Tasks exceeding this are killed and marked failed. Per-task `timeout` field overrides this. |
| `defaults.max_retries` | `int` | `0` | Number of times to retry a failed task. Per-task `max_retries` field overrides this. |
| `defaults.retry_backoff` | `string` | `"30s"` | Go duration to wait before retrying a failed task. |
| `defaults.shutdown_grace` | `string` | `"10s"` | Go duration to wait after SIGTERM before sending SIGKILL during task cancellation. |
| `defaults.log_retention_days` | `int` | `7` | Number of days to keep log files, reports, and workdirs. Cleanup runs at orchestrator startup. |
| `notifications.desktop` | `bool` | `false` | Enable desktop notifications on task completion/failure/timeout. Uses `osascript` on macOS, `notify-send` on Linux. |

## Task Model

Tasks have these fields (stored in job JSON files):

| Field | Type | Description |
|---|---|---|
| `id` | `string` | Auto-incrementing task ID |
| `prompt` | `string` | The task prompt |
| `schedule` | `string` | Schedule expression (optional) |
| `status` | `string` | `pending`, `running`, `completed`, `failed`, `cancelled` |
| `created_at` | `time` | When the task was created |
| `started_at` | `time` | When the task started running |
| `finished_at` | `time` | When the task finished |
| `error` | `string` | Error message (for failed tasks) |
| `pid` | `int` | Process ID of the running agent |
| `max_retries` | `int` | Per-task retry override |
| `retry_count` | `int` | Number of retries attempted so far |
| `retry_after` | `time` | Earliest time this task can be retried |
| `timeout` | `string` | Per-task timeout override (Go duration) |

## Task Lifecycle

1. Task created (status: `pending`) — via `orc add` or by writing to `jobs/inbox/`
2. Orchestrator picks it up when a slot is available (status: `running`), PID recorded
3. Agent command runs in `.orc/workdirs/<task-id>/`, stdout/stderr streamed to `output.log`
4. On completion: task marked `completed` or `failed`
5. On failure with retries remaining: task reset to `pending` with `retry_after` set
6. On timeout: task killed (SIGTERM→SIGKILL) and marked `failed` with timeout error
7. Entry recorded in daily report (`reports/YYYY-MM-DD.json`)
8. Desktop notification sent (if enabled)
9. For scheduled tasks: reset to `pending` when next run time arrives

Task statuses: `pending`, `running`, `completed`, `failed`, `cancelled`

## TUI Keybindings

### Dashboard

| Key | Action |
|---|---|
| `↑`/`k` | Navigate up |
| `↓`/`j` | Navigate down |
| `enter` | View task output / notification details |
| `x` | Kill selected running task (with confirmation) |
| `q` / `ctrl+c` | Quit (press twice to confirm) |

### Task Output View

| Key | Action |
|---|---|
| `↑`/`k` | Scroll up |
| `↓`/`j` | Scroll down |
| `g` | Jump to top |
| `G` | Jump to bottom |
| `/` | Open search |
| `n` | Next search match |
| `N` | Previous search match |
| `esc` | Clear search / go back |
| `q` | Go back to dashboard |

### TUI Features

- **Live agent status**: Shows the latest output line from each running task under its entry in the dashboard.
- **Notifications section**: Shows tasks that completed with a `NOTES.md` file (user action required) and tasks with uncommitted git changes in their workdir.
- **Git change detection**: After a task completes, the TUI checks for uncommitted git changes in the task's workdir and shows a notification with the diff stat.
- **Output search**: In the task output view, press `/` to search. Matches are highlighted. Use `n`/`N` to navigate between matches.

## Logging

Log lines use tagged prefixes for filtering:
- `[orc]` — orchestrator events (startup, shutdown, task added from inbox)
- `[sched]` — schedule events (task re-queued)
- `[task:<id>]` — per-task events (start, complete, fail, retry, timeout)

Per-task logs are also written to `logs/task-<id>.log` for quick access.

The `orc log` command supports:
- `--level orc` — show only orchestrator/scheduler events
- `--level task` — show only task events
- `--output-only` — show only agent stdout/stderr (with `-t`)

## Schedule Formats

- `every 5m` / `every 1h` / `every 30s` — interval-based (Go duration)
- `daily 09:00` — daily at specific time
- `hourly` — every hour on the hour

## Dependencies

- `github.com/charmbracelet/bubbletea` — TUI framework
- `github.com/charmbracelet/lipgloss` — TUI styling
- `github.com/spf13/cobra` — CLI framework
- `github.com/tidwall/jsonc` — JSONC config parsing
- Go stdlib for everything else (no external scheduler, no socket libs)

## Known Limitations / Future Work

- Schedule parsing is simple string matching, not full cron syntax
- No task dependencies or DAG ordering
- No `orc edit` command to modify existing tasks
- Log streaming (`orc log -f`) uses file polling, not inotify
