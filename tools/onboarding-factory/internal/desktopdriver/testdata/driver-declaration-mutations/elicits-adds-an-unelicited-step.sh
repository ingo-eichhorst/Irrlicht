#!/usr/bin/env bash
# MUTATION: `slash` added to DRIVE_ELICITS. This is the dangerous direction —
# recipe-lint would pass a slash recipe straight through to a live Desktop run
# that stores the command as prompt text and records a green no-op.
DRIVE_ELICITS="archive interrupt keys mode model send slash sleep start_session wait_turn"
DRIVE_MISSING_CONTROLS="exit_clean:session-exit reset_session:session-reset restart:session-restart resume:session-resume session:session-list-row sigkill:agent-process-kill slash:slash-command-entry"
DRIVE_SLASH_REQUIRES_STEP_TYPE=true
