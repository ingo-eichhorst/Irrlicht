#!/usr/bin/env bash
# MUTATION: `reset_session` added to DRIVE_ELICITS while the Go driver still
# lists it as missing. This is the dangerous direction — recipe-lint would pass
# the recipe straight through to a live Desktop run that has no control for the
# step, and the run would refuse only after it had opened a real session.
#
# This fixture used to add `slash` here. That stopped being a mutation on
# 2026-09-07, when the driver grew a slash step and the declaration gained it
# for real, which is exactly the drift these fixtures exist to catch.
DRIVE_ELICITS="archive interrupt keys mode model reset_session send slash sleep start_session wait_turn"
DRIVE_MISSING_CONTROLS="exit_clean:session-exit reset_session:session-reset restart:session-restart resume:session-resume session:session-list-row sigkill:agent-process-kill"
DRIVE_SLASH_REQUIRES_STEP_TYPE=true
