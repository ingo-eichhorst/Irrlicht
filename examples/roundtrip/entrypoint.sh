#!/usr/bin/env bash
# Round-trip agent entrypoint: start irrlichd in the background (logging to the
# container's stdout, so `docker compose logs agent` shows it), then hold the
# container open so the user drives `claude` via `docker compose exec`.
set -euo pipefail

echo "[entrypoint] starting irrlichd → relay ${IRRLICHT_RELAY_URL:-<unset>}"
irrlichd &
daemon=$!

# If the daemon dies, say so on stderr instead of idling silently — otherwise
# `sleep infinity` keeps the container "healthy" while nothing is observed and
# "nothing shows up on the Mac" is undebuggable.
(
  while kill -0 "$daemon" 2>/dev/null; do sleep 5; done
  echo "[entrypoint] WARNING: irrlichd exited — container still up for debugging" >&2
) &

# Keep the container alive for interactive `claude` sessions.
exec sleep infinity
