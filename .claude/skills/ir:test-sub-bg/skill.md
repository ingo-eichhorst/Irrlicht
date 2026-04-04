---
name: ir:test-bg
description: >
  Test background agent detection in irrlicht. Launches 6 background Explore
  agents in 2 waves (3 + 3). Use when the user says "test background", "test bg",
  or "/ir:test-bg".
---

# Test Background Agent Detection

Launch background agents in 2 waves to verify irrlicht detects and displays them correctly.
Run both waves back-to-back without pausing for user confirmation. Do NOT run any checker process, curl commands, or daemon API queries between waves — just launch the agents and print status messages.

## Wave 1: 3 Background Agents

Launch 3 Agent tool calls in a single message with `run_in_background: true`:

1. **Agent 1** (subagent_type: Explore, run_in_background: true): "List all .go files in core/adapters/inbound/ recursively. Report file count and directory structure."
2. **Agent 2** (subagent_type: Explore, run_in_background: true): "Find all struct definitions in core/domain/session/. Report their names and file locations."
3. **Agent 3** (subagent_type: Explore, run_in_background: true): "Search for all HTTP handler functions in core/cmd/irrlichd/main.go. Report their names and line numbers."

Immediately after launching (don't wait for completion), print "Wave 1 launched (3 background agents)." Then immediately proceed to Wave 2.

## Wave 2: 3 More Background Agents

Launch 3 Agent tool calls in a single message with `run_in_background: true`:

1. (subagent_type: Explore, run_in_background: true): "Find all TODO comments in the core/ directory. Report file, line number, and text."
2. (subagent_type: Explore, run_in_background: true): "Search for all error handling patterns (fmt.Errorf) in core/application/. Report count and examples."
3. (subagent_type: Explore, run_in_background: true): "Find all goroutine launches (go func, go d., etc.) in core/. Report locations."

Immediately after launching, print "All waves launched (6 background agents). Check the UI for the full picture."
