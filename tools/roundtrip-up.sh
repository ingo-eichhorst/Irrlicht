#!/usr/bin/env bash
# roundtrip-up.sh — bring up the live cross-host round-trip testbed.
# Thin wrapper around the compose file so it's discoverable from tools/.
# See examples/roundtrip/README.md for the full demo.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
COMPOSE="$SCRIPT_DIR/../examples/roundtrip/docker-compose.yml"

docker compose -f "$COMPOSE" up -d --build "$@"

cat <<'EOF'

Up. Next:
  • Mac app: Settings → Sources → add ws://localhost:7839 (or open http://localhost:7839)
  • Drive claude:
      docker compose -f examples/roundtrip/docker-compose.yml exec -it agent claude
EOF
