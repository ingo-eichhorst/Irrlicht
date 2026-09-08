#!/usr/bin/env bash
# MUTATION: session-list-row renamed to sidebar-row. The refusal would still
# fire, but name a control nobody can look up against the measured dump.
DRIVE_ELICITS="archive interrupt keys mode model send slash sleep start_session wait_turn"
DRIVE_MISSING_CONTROLS="exit_clean:session-exit reset_session:session-reset restart:session-restart resume:session-resume session:sidebar-row sigkill:agent-process-kill"
DRIVE_SLASH_REQUIRES_STEP_TYPE=true
