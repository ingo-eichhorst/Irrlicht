#!/bin/bash
# validate.sh — single entry point for agent and CI validation
# Exit 0 only when the app satisfies its full contract.
# Run after every code change: ./validate.sh
set -euo pipefail

echo "==> Sync: Web frontend → embed directory"
cp frontend/web/index.html core/cmd/irrlichtd/ui/index.html

echo "==> Build: Go core"
cd core && go build ./... && cd ..

echo "==> Build: SwiftUI app"
cd frontend/macos && swift build 2>&1 && cd ../..

echo "==> Validate: Web frontend"
test -s frontend/web/index.html || { echo "FAIL: frontend/web/index.html missing or empty"; exit 1; }
grep -q 'api/v1/sessions' frontend/web/index.html || { echo "FAIL: index.html missing API endpoint"; exit 1; }
grep -q 'api/v1/sessions/stream' frontend/web/index.html || { echo "FAIL: index.html missing WebSocket endpoint"; exit 1; }

echo "==> Test: Go components"
cd core && go test ./... && cd ..

echo "==> Test: SwiftUI components"
cd frontend/macos && swift test 2>&1 && cd ../..

echo ""
echo "All checks passed."
