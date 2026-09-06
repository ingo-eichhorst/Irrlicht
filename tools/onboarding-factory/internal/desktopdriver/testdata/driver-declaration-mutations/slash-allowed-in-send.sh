#!/usr/bin/env bash
# MUTATION: DRIVE_SLASH_REQUIRES_STEP_TYPE flipped to false, which lets a recipe
# smuggle "/model sonnet" through a `send` step past recipe-lint.
DRIVE_ELICITS="archive interrupt keys mode model send sleep start_session wait_turn"
DRIVE_MISSING_CONTROLS="exit_clean:session-exit reset_session:session-reset restart:session-restart resume:session-resume session:session-list-row sigkill:agent-process-kill slash:slash-command-entry"
DRIVE_SLASH_REQUIRES_STEP_TYPE=false
