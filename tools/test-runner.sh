#!/bin/bash

# Test runner for Irrlicht components
set -e

echo "Running Irrlicht Test Suite"
echo "================================"

# Track test results
failed_tests=0
total_tests=0

# Function to run tests and capture results
run_test() {
    local test_name="$1"
    local command="$2"

    echo ""
    echo "Running $test_name..."
    echo "$(printf '─%.0s' {1..50})"

    if eval "$command"; then
        echo "PASS: $test_name"
        return 0
    else
        echo "FAIL: $test_name"
        return 1
    fi
}

# Test 1: Go unit tests
((total_tests++))
if ! run_test "Go Unit Tests" "cd core && go test -v ./..."; then
    ((failed_tests++))
fi

# Test 2: Go build (daemon)
((total_tests++))
if ! run_test "Go Build (irrlichtd)" "cd core && go build ./cmd/irrlichtd/"; then
    ((failed_tests++))
fi

# Test 3: SwiftUI tests
((total_tests++))
if ! run_test "SwiftUI Tests" "cd platforms/macos && swift test"; then
    ((failed_tests++))
fi

# Summary
echo ""
echo "Test Summary"
echo "==============="
echo "Total tests: $total_tests"
echo "Passed: $((total_tests - failed_tests))"
echo "Failed: $failed_tests"

if [ $failed_tests -eq 0 ]; then
    echo ""
    echo "All tests passed!"
    exit 0
else
    echo ""
    echo "Some tests failed. Please check the output above."
    exit 1
fi
