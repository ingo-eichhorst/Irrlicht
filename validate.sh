#!/bin/bash
# validate.sh — single entry point for agent and CI validation
# Exit 0 only when the app satisfies its full contract.
# Run after every code change: ./validate.sh
set -euo pipefail

echo "==> Build: Go core"
cd core && go build ./... && cd ..

echo "==> Build: SwiftUI app"
cd frontend/macos && swift build 2>&1 && cd ../..

echo "==> Test: Go components"
cd core && go test ./... && cd ..

echo "==> Test: SwiftUI components"
cd frontend/macos && swift test 2>&1 && cd ../..

echo ""
echo "All checks passed."
