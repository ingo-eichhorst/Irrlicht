---
name: ir:test-fg
description: >
  Test foreground subagent detection in irrlicht. Launches 6 foreground Explore
  agents in 2 waves (3 + 3). Use when the user says "test foreground", "test fg",
  or "/ir:test-fg".
---

# Test Foreground Subagent Detection

Launch foreground agents in 2 waves to verify irrlicht detects and displays them correctly.
Run both waves back-to-back without pausing for user confirmation. Do NOT run any checker process, curl commands, or daemon API queries between waves — just launch the agents and print status messages.

## Wave 1: 3 Foreground Subagents

Launch 3 Agent tool calls in a single message (parallel). Each agent should do a small read-only task that takes ~10 seconds:

1. **Agent 1** (subagent_type: Explore): "Count the number of Go files in the core/ directory. Report the total."
2. **Agent 2** (subagent_type: Explore): "Find all test files in core/e2e/ and list their names. Report the list."
3. **Agent 3** (subagent_type: Explore): "Search for all exported functions in core/domain/session/session.go. Report their names."

After all 3 complete, print "Wave 1 done (3 foreground subagents)." Then immediately proceed to Wave 2.

## Wave 2: 3 More Foreground Subagents

Launch 3 Agent tool calls in a single message (parallel):

1. (subagent_type: Explore): "Find all imports of the 'sync' package across core/. Report the file list."
2. (subagent_type: Explore): "Count the total lines of Go code in core/pkg/tailer/. Report the count."
3. (subagent_type: Explore): "List all interface definitions in core/ports/. Report their names and methods."

After all 3 complete, print "All waves complete (6 foreground subagents). Check the UI for the full picture."
