# Fully Automated Agent Onboarding Pipeline

## Problem

Adding support for a new agent (like Claude Code, Codex, Pi) or orchestrator (like Gastown) currently requires manual effort to understand the agent's behavior, write the adapter, and create test cases. There is no automated pipeline that can:

1. Spin up the actual agent in a controlled environment
2. Run it through all relevant scenarios
3. Record the resulting sessions
4. Generate e2e test cases from those recorded sessions

## Proposal

Build an **automated agent onboarding skill** that can fully onboard a new agent or orchestrator adapter by controlling the agent via **tmux** and/or **computer use**, running it through scenarios, and generating e2e tests from the observed sessions.

### Architecture

```
┌─────────────────────────────────────────────────┐
│              Onboarding Skill                   │
│  (Claude Code skill / CLI entrypoint)           │
├─────────────────────────────────────────────────┤
│                                                 │
│  1. Agent Controller (tmux / computer use)      │
│     - Launch agent in isolated tmux session     │
│     - Send commands / keystrokes                │
│     - Observe agent output via screen capture   │
│     - Detect agent state transitions            │
│                                                 │
│  2. Scenario Runner                             │
│     - Select relevant scenarios for agent type  │
│     - Execute each scenario sequentially        │
│     - Record session artifacts (JSONL, PIDs,    │
│       process trees, state transitions)         │
│                                                 │
│  3. E2E Test Generator                          │
│     - Parse recorded session data               │
│     - Generate Go test cases matching           │
│       core/e2e/ conventions                     │
│     - Include parser test fixtures from real     │
│       transcript data                           │
│                                                 │
└─────────────────────────────────────────────────┘
```

### Scenarios to Cover

The following scenarios must be available and selectable per agent/orchestrator type:

#### Agent Adapter Scenarios
| # | Scenario | Description | Relevant Adapters |
|---|----------|-------------|-------------------|
| 1 | **Cold start** | Agent process starts, no prior session exists | `claudecode`, `codex`, `pi` |
| 2 | **Pre-session detection** | Process detected before transcript file appears (existing e2e: `presession_test.go`) | `processlifecycle` → all agents |
| 3 | **Active working session** | Agent is actively generating output / running tools | `claudecode`, `codex`, `pi` |
| 4 | **Idle / waiting session** | Agent is waiting for user input | `claudecode`, `codex`, `pi` |
| 5 | **Session end / process exit** | Agent process terminates cleanly | `claudecode`, `codex`, `pi` |
| 6 | **Crash / abnormal exit** | Agent process dies unexpectedly | `processlifecycle` |
| 7 | **Multiple concurrent sessions** | Two+ agent instances running simultaneously | `claudecode`, `codex` |
| 8 | **Parent-child sessions** | Agent spawns subagent (ParentSessionID linking) | `claudecode` |
| 9 | **Transcript parsing** | Validate JSONL/log parsing against real output | `claudecode`, `codex`, `pi` |
| 10 | **PID resolution** | Correct PID extraction and CWD detection | `claudecode`, `codex`, `pi` |
| 11 | **File system watching** | Detect sessions via file system events | `fswatcher` |

#### Orchestrator Adapter Scenarios
| # | Scenario | Description | Relevant Adapters |
|---|----------|-------------|-------------------|
| 1 | **Agent discovery** | Orchestrator polls and discovers running agents | `gastown` |
| 2 | **Role assignment** | Agents are assigned roles by orchestrator | `gastown` |
| 3 | **State aggregation** | Orchestrator collects state from multiple agents | `gastown` |
| 4 | **Agent join/leave** | Agents dynamically join or leave the orchestration | `gastown` |
| 5 | **Polling lifecycle** | Poller start → collect → aggregate → report cycle | `gastown` |

### Agent Controller Requirements

The skill must be able to **control agents programmatically**:

- **tmux**: Launch agents in named tmux sessions, send keys (`tmux send-keys`), capture panes (`tmux capture-pane`), monitor output
- **Computer use**: For GUI-based agents or scenarios requiring visual interaction (e.g., IDE-integrated agents), use Anthropic computer use to observe and interact with the screen
- **Process monitoring**: Track PIDs, process trees, and CWD changes during scenarios via the existing `processlifecycle` adapter primitives (`scanner.go`, `discovery.go`)

### E2E Test Generation

From recorded sessions, generate:

1. **Parser test fixtures** — real JSONL snippets → expected parse results (like existing `parser_test.go` in each adapter)
2. **PID resolution tests** — recorded process trees → expected PID extraction
3. **Full pipeline e2e tests** — following `core/e2e/presession_test.go` patterns: start real process → wire up pipeline → assert session states
4. **Orchestrator integration tests** — recorded poll cycles → expected role assignments and state aggregation

### Implementation Steps

1. [ ] Define scenario interface (`Scenario` with `Name`, `Setup`, `Run`, `Teardown`, `Verify`)
2. [ ] Implement tmux-based agent controller
3. [ ] Implement computer-use agent controller (for GUI scenarios)
4. [ ] Build scenario registry with all scenarios listed above
5. [ ] Build session recorder that captures all artifacts during scenario execution
6. [ ] Build e2e test generator that produces Go test files from recorded sessions
7. [ ] Create Claude Code skill (`/ir:onboard-agent`) as the entrypoint
8. [ ] Validate by onboarding an existing adapter (e.g., `claudecode`) end-to-end
9. [ ] Document the onboarding process and scenario authoring

### Success Criteria

- Running `/ir:onboard-agent claudecode` produces a full set of e2e tests covering all relevant scenarios
- Running `/ir:onboard-agent gastown` produces orchestrator-specific e2e tests
- A new agent can be onboarded by simply defining its launch command and selecting applicable scenarios
- Generated tests pass `go test ./...` and follow project conventions

### Related Files

- `core/adapters/inbound/agents/claudecode/` — Claude Code adapter (parser, PID, adapter)
- `core/adapters/inbound/agents/codex/` — Codex adapter
- `core/adapters/inbound/agents/pi/` — Pi adapter
- `core/adapters/inbound/agents/fswatcher/` — File system watcher adapter
- `core/adapters/inbound/agents/processlifecycle/` — Process lifecycle detection
- `core/adapters/inbound/orchestrators/gastown/` — Gastown orchestrator adapter
- `core/e2e/presession_test.go` — Existing e2e test pattern
- `core/domain/session/` — Session domain model
- `core/domain/agent/` — Agent event model
- `core/domain/orchestrator/` — Orchestrator state model
