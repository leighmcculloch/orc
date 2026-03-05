# Orc — AI Agent Orchestrator

A terminal CLI app (Go) that orchestrates a fleet of AI coding agent processes. It runs as a long-running foreground app with a TUI. CLI commands read/write shared job files directly.

## Build & Run

```bash
cd orc
go build -o orc .
./orc init          # creates .orc/ directory with default config
./orc run           # starts orchestrator with TUI (foreground)
```

## Commands

```
orc run                                   Start orchestrator (foreground TUI)
orc add <prompt>                          Add ad-hoc task
orc add -e <env> <prompt>                 Add task with environment
orc add -s <schedule> <prompt>            Add scheduled task
orc add -e <env> -s <schedule> <prompt>   Both
orc list | orc ls                         List all tasks
orc remove <id> | orc rm <id>            Remove a task
orc status                                Show running/pending/completed counts
orc log [-d YYYY-MM-DD] [-f] [-t <id>]   View/stream logs
orc report [today|yesterday|YYYY-MM-DD]   View completed task reports
orc init                                  Initialize .orc/ directory
```

## Architecture

```
orc/
├── main.go                              CLI entry point, all commands
├── config/config.go                     Config types, load/save, .orc dir management, pid file
├── state/state.go                       Task model, Store with mutex-protected JSON persistence
├── orchestrator/orchestrator.go         Core engine: main loop, task dispatch, inbox polling
├── orchestrator/schedule.go             Schedule parsing
├── agent/agent.go                       Agent process lifecycle (exec, subtask support)
├── logging/logging.go                   Per-day + per-task log files, streaming
├── report/report.go                     Daily report catalogue (completed task summaries)
├── tui/tui.go                           Bubbletea TUI dashboard
└── pick/pick.go                         Interactive task picker
```

## .orc/ Directory Layout

All state and config lives in `.orc/` in the current working directory:

```
.orc/
├── config.jsonc          # environments, defaults (max_concurrent, command)
├── jobs/
│   ├── meta.json        # next task ID counter
│   ├── todo.json        # pending + running tasks
│   ├── scheduled.json   # scheduled tasks
│   ├── completed.json   # completed tasks
│   ├── failed.json      # failed + cancelled tasks
│   └── inbox/           # prompt files dropped by orc-add for subtask creation
├── orc.pid              # pid file, exists only while orchestrator is running
├── bin/
│   └── orc-add          # helper script for agents to create subtasks
├── logs/
│   ├── orc-YYYY-MM-DD.log    # daily orchestrator log
│   └── task-<id>.log         # per-task log
├── workdirs/
│   └── <task-id>/
│       ├── status.json        # live process status (written by agent runner)
│       ├── output.log         # full agent stdout/stderr capture
│       └── prompt.txt         # prompt sent to the agent
└── reports/
    └── YYYY-MM-DD.json        # daily catalogue of completed task entries
```

## Key Design Decisions

- **Shared job files, no IPC.** CLI commands and the orchestrator read/write the same job files directly. No inbox/outbox protocol needed.
- **pid file for liveness.** `.orc/orc.pid` is created on `orc run` and removed on shutdown. `config.IsRunning()` checks the pid file and validates the process is alive.
- **All JSON.** Config, job files, status files, reports — everything is JSON.
- **Tasks stored in separate files by status.** `jobs/todo.json`, `jobs/scheduled.json`, `jobs/completed.json`, `jobs/failed.json`. `state.Store` uses `sync.Mutex` for concurrent updates. All writes are atomic (tmp+rename).
- **Agent command is configurable.** Set `defaults.command` in config. The `$prompt` placeholder is replaced with the task prompt. No default — must be configured.
- **Tasks can create subtasks.** Orc instructions are appended to every prompt telling agents how to use `.orc/bin/orc-add "prompt"`. The script drops a prompt file into `jobs/inbox/` which the orchestrator picks up.
- **Scheduled tasks stay in the task list.** After completing, the orchestrator resets them to pending when the next scheduled time arrives.
- **TUI uses bubbletea with alt screen.** Refreshes every 1s via tick, receives events from orchestrator via channel.

## Config Format (.orc/config.jsonc)

```json
{
  "environments": {
    "default": {
      "name": "default",
      "work_dir": "."
    }
  },
  "defaults": {
    "environment": "default",
    "max_concurrent": 1,
    "command": "claude -p \"$prompt\" --dangerously-skip-permissions"
  }
}
```

The `command` is run via `sh -c` with `$prompt` replaced by the task prompt. This field is required — orc will error if it is not set.

### Examples

Using Claude Code directly:
```json
"command": "claude -p \"$prompt\" --dangerously-skip-permissions"
```

Using [silo](https://github.com/leighmcculloch/silo) for container isolation:
```json
"command": "silo claude -v -- -p \"$prompt\" --dangerously-skip-permissions"
```

Using silo with GitHub Copilot:
```json
"command": "silo copilot -v -- --model claude-opus-4.6 --allow-all-tools -p \"$prompt\""
```

## Task Lifecycle

1. Task created (status: `pending`) — via `orc add` or `orc-add`
2. Orchestrator picks it up when a slot is available (status: `running`)
3. Agent command runs, stdout streamed to log
4. On completion, task marked `completed` or `failed`
5. Entry recorded in daily report (`reports/YYYY-MM-DD.json`)
6. For scheduled tasks: reset to `pending` when next run time arrives

Task statuses: `pending`, `running`, `completed`, `failed`, `cancelled`

## Schedule Formats

- `every 5m` / `every 1h` / `every 30s` — interval-based (Go duration)
- `daily 09:00` — daily at specific time
- `hourly` — every hour on the hour

## Dependencies

- `github.com/charmbracelet/bubbletea` — TUI framework
- `github.com/charmbracelet/lipgloss` — TUI styling
- Go stdlib for everything else (no external scheduler, no socket libs)

## Known Limitations / Future Work

- Schedule parsing is simple string matching, not full cron syntax
- No task dependencies or DAG ordering
- No retry logic for failed tasks
- No task timeout/kill mechanism
- No `orc edit` command to modify existing tasks
- Log streaming (`orc log -f`) uses file polling, not inotify
