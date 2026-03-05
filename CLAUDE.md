# Orc — Claude Code Orchestrator

A terminal CLI app (Go) that orchestrates a fleet of Claude Code processes. It runs as a long-running foreground app with a TUI, and other `orc` processes communicate with it via file-based IPC.

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
orc stop                                  Stop running orchestrator
```

## Architecture

```
orc/
├── main.go                              CLI entry point, all commands
├── internal/
│   ├── config/config.go                 Config types, load/save, .orc dir management
│   ├── state/state.go                   Task model, Store with mutex-protected JSON persistence
│   ├── orchestrator/orchestrator.go     Core engine: main loop, inbox polling, task dispatch
│   ├── orchestrator/schedule.go         Schedule parsing + ID generation
│   ├── claude/claude.go                 Claude Code process lifecycle (pre-hooks, exec, report)
│   ├── ipc/ipc.go                       File-based IPC (inbox/outbox JSON files, pid file)
│   ├── logging/logging.go              Per-day + per-task log files, streaming
│   ├── report/report.go                Daily report catalogue (completed task summaries)
│   └── tui/tui.go                      Bubbletea TUI dashboard
```

## .orc/ Directory Layout

All state and config lives in `.orc/` in the current working directory:

```
.orc/
├── config.json          # environments, defaults (max_concurrent, agent_command)
├── state.json           # task list with statuses (atomic write via tmp+rename)
├── orc.pid              # pid file, exists only while orchestrator is running
├── bin/
│   └── orc-add              # helper script for agents to create subtasks
├── inbox/               # IPC: CLI writes command .json files here
├── outbox/              # IPC: orchestrator writes response .json files here
├── logs/
│   ├── orc-YYYY-MM-DD.log    # daily orchestrator log
│   └── task-<id>.log         # per-task log
├── workdirs/
│   └── <task-id>/
│       ├── status.json        # live process status (written by claude runner)
│       ├── output.log         # full claude stdout/stderr capture
│       └── report.md          # summary report written by claude agent
└── reports/
    └── YYYY-MM-DD.json        # daily catalogue of completed task entries
```

## Key Design Decisions

- **File-based IPC, not sockets.** CLI writes JSON command files to `.orc/inbox/`, orchestrator polls every 1s, processes them, writes responses to `.orc/outbox/`. CLI polls outbox for response (100ms interval, 30s timeout). Atomic writes via tmp+rename prevent partial reads.
- **pid file for liveness.** `.orc/orc.pid` is created on `orc run` and removed on shutdown. `ipc.IsRunning()` checks for this file.
- **All JSON.** Config, state, IPC messages, status files, reports — everything is JSON.
- **State file is mutex-protected.** `state.Store` uses `sync.Mutex` for concurrent task updates from goroutines. Writes are atomic (tmp+rename).
- **Agent command is configurable.** Defaults to `silo claude -- -p "$prompt"`. The `$prompt` placeholder is replaced with the task prompt.
- **Tasks can create subtasks.** Orc instructions are appended to every prompt telling agents how to use `.orc/bin/orc-add "prompt"` to submit new tasks. The helper script writes IPC JSON to the inbox.
- **Pre-hooks run before the agent.** Each environment config has `pre_hooks` (shell commands) that execute in the environment's `work_dir` before the agent starts.
- **Scheduled tasks stay in the task list.** After completing, the orchestrator resets them to pending when the next scheduled time arrives.
- **TUI uses bubbletea with alt screen.** Refreshes every 1s via tick, receives events from orchestrator via channel.

## Config Format (.orc/config.json)

```json
{
  "environments": {
    "default": {
      "name": "default",
      "work_dir": ".",
      "pre_hooks": []
    },
    "myproject": {
      "name": "myproject",
      "work_dir": "/path/to/project",
      "pre_hooks": ["git pull", "npm install"]
    }
  },
  "defaults": {
    "environment": "default",
    "max_concurrent": 3,
    "agent_command": "silo claude -- -p \"$prompt\""
  }
}
```

The `agent_command` is run via `sh -c` with `$prompt` replaced by the task prompt. To use a different agent, change the command. For example, to use GitHub Copilot:

```json
{
  "defaults": {
    "agent_command": "silo copilot -- -p \"$prompt\""
  }
}
```

## Task Lifecycle

1. Task created (status: `pending`) — via `orc add` or IPC
2. Orchestrator picks it up when a slot is available (status: `running`)
3. Pre-hooks execute in environment's work_dir
4. Agent command runs, stdout streamed to log
5. On completion, task marked `completed` or `failed`
6. Entry recorded in daily report (`reports/YYYY-MM-DD.json`)
7. For scheduled tasks: reset to `pending` when next run time arrives

Task statuses: `pending`, `running`, `completed`, `failed`, `cancelled`

## Schedule Formats

- `every 5m` / `every 1h` / `every 30s` — interval-based (Go duration)
- `daily 09:00` — daily at specific time
- `hourly` — every hour on the hour

## IPC Protocol

Commands (written as JSON to inbox): `add_task`, `list_tasks`, `remove_task`, `get_status`, `stop`

Each request file has an `id` field. Response is written to `outbox/<id>.json` with `ok`, `error`, and `payload` fields.

When orc is not running, `orc add` and `orc list` fall back to reading/writing `state.json` directly.

## Dependencies

- `github.com/charmbracelet/bubbletea` — TUI framework
- `github.com/charmbracelet/lipgloss` — TUI styling
- Go stdlib for everything else (no external scheduler, no socket libs)

## Known Limitations / Future Work

- Schedule parsing is simple string matching, not full cron syntax
- No task dependencies or DAG ordering
- No retry logic for failed tasks
- No task timeout/kill mechanism
- pid file liveness check doesn't verify the process is actually alive
- No `orc edit` command to modify existing tasks
- TUI doesn't support scrolling or task detail view
- Log streaming (`orc log -f`) uses file polling, not inotify
- `generateID()` in main.go uses time-based bytes (less random than orchestrator's crypto/rand version)
