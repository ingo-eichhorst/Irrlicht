#!/usr/bin/env bash
# One-command bootstrap for the coding-factory demo: bring up the auth-enabled
# relay, mint a bearer token, then start the three codex agents sharing it.
#
#   OPENAI_API_KEY=sk-... ./up.sh
#   # ...or put OPENAI_API_KEY in .env first.
set -euo pipefail
cd "$(dirname "$0")"

compose() { docker compose -f docker-compose.yml "$@"; }

[ -f .env ] || cp .env.example .env

# OPENAI_API_KEY may come from the environment or .env; make sure it's in .env so
# compose (which reads .env) sees it.
if ! grep -q '^OPENAI_API_KEY=.\+' .env; then
  if [ -n "${OPENAI_API_KEY:-}" ]; then
    grep -v '^OPENAI_API_KEY=' .env > .env.tmp || true
    { cat .env.tmp; echo "OPENAI_API_KEY=$OPENAI_API_KEY"; } > .env
    rm -f .env.tmp
  else
    echo "error: set OPENAI_API_KEY (in the environment or .env) — codex needs it." >&2
    exit 1
  fi
fi

echo "[up] starting the relay…"
# A placeholder token lets compose interpolate while we bring up only the relay.
IRRLICHT_RELAY_TOKEN=bootstrap compose up -d --build relay

echo "[up] issuing a bearer token…"
issue="$(compose exec -T relay sh -c \
  'IRRLICHT_HOME=/var/lib/irrlichtrelay irrlichtrelay token issue --label coding-factory')"
# The plaintext token is the indented second line of the issue output.
token="$(printf '%s\n' "$issue" | awk 'NR==2 {print $1}')"
if [ -z "$token" ]; then
  echo "error: could not parse the issued token from:" >&2
  printf '%s\n' "$issue" >&2
  exit 1
fi

# Persist the token into .env for the agents (and future `docker compose` runs).
grep -v '^IRRLICHT_RELAY_TOKEN=' .env > .env.tmp || true
{ cat .env.tmp; echo "IRRLICHT_RELAY_TOKEN=$token"; } > .env
rm -f .env.tmp

echo "[up] starting the 3 codex agents…"
compose up -d --build codex-1 codex-2 codex-3

cat <<EOF

[up] coding factory is up.
     Dashboard:  http://localhost:7839
     Relay URL:  ws://localhost:7839     (add as a Source on macOS/web)
     Token:      $token

Watch three rows — codex-1, codex-2, codex-3 — appear with the remote (cloud)
origin glyph, cycling working→ready over the authenticated relay. Attach to one:
     docker compose -f examples/coding-factory/docker-compose.yml exec -it codex-1 tmux attach -t codex
EOF
