#!/usr/bin/env bash
# Coding-factory agent entrypoint: give this container a fresh relay identity,
# point codex at a small model, start irrlichd in the background (logging to the
# container's stdout), then hand off to the tmux auto-driver.
set -euo pipefail

state_dir="${IRRLICHT_HOME:-$HOME/.local/share/irrlicht}"

# Fresh daemon_id per container so the three agents are three distinct rows on
# the Mac/web — never share relay-identity.json across replicas (#539/item E).
rm -f "$state_dir/relay-identity.json"

# Codex reads its model from ~/.codex/config.toml; auth rides OPENAI_API_KEY
# from the environment (no key on disk).
mkdir -p "$HOME/.codex"
printf 'model = "%s"\n' "${CODEX_MODEL:-gpt-4o-mini}" > "$HOME/.codex/config.toml"

# Pre-authenticate codex so the first-run API key wizard doesn't appear in the
# tmux session before the driver is ready to handle it. `--with-api-key` reads
# from stdin and stores the key in ~/.codex/; OPENAI_API_KEY in the env suffices
# for inference itself, but without this the interactive wizard fires first.
if [ -n "${OPENAI_API_KEY:-}" ]; then
  echo "$OPENAI_API_KEY" | codex login --with-api-key 2>/dev/null || true
fi

echo "[entrypoint] irrlichd → ${IRRLICHT_RELAY_URL:-<unset>} (token: ${IRRLICHT_RELAY_TOKEN:+set}, model: ${CODEX_MODEL:-gpt-4o-mini})"
irrlichd &
daemon=$!

# If the daemon dies, say so on stderr instead of failing silently — otherwise
# "nothing shows up on the Mac" is undebuggable.
(
  while kill -0 "$daemon" 2>/dev/null; do sleep 5; done
  echo "[entrypoint] WARNING: irrlichd exited — container still up for debugging" >&2
) &

# Drive codex in tmux (keeps the container alive; attach with
# `docker compose exec -it <svc> tmux attach -t codex`).
exec /usr/local/bin/drive.sh
