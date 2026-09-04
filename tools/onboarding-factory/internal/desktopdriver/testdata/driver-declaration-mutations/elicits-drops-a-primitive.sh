#!/usr/bin/env bash
# MUTATION: `mode` removed from DRIVE_ELICITS while the Go driver still elicits
# it. recipe-lint would then refuse a mode step the driver can actually drive.
DRIVE_ELICITS="archive interrupt keys model send sleep start_session wait_turn"
DRIVE_MISSING_CONTROLS="exit_clean:session-exit reset_session:session-reset restart:session-restart resume:session-resume session:session-list-row sigkill:agent-process-kill slash:slash-command-entry"
DRIVE_SLASH_REQUIRES_STEP_TYPE=true
