#!/bin/bash

# Test runner for Irrlicht components
set -e

echo "Running Irrlicht Test Suite"
echo "================================"

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

# Track test results
failed_tests=0
total_tests=0

# Test 1: Fixture validation
((total_tests++))
if ! run_test "Fixture Validation" "./tools/irrlicht-replay --validate-only fixtures/session-start.json"; then
    ((failed_tests++))
fi

# Test 2: Edge case validation
((total_tests++))
if ! run_test "Edge Case Validation" "./tools/irrlicht-replay --validate-only fixtures/edge-cases/malformed-json.txt"; then
    # This should fail, so invert the logic
    echo "PASS: Edge Case Validation (correctly rejected malformed JSON)"
else
    echo "FAIL: Edge Case Validation (should have rejected malformed JSON)"
    ((failed_tests++))
fi

# Test 3: Concurrency scenario validation
((total_tests++))
if ! run_test "Concurrent Scenarios Validation" "
    ./tools/irrlicht-replay --validate-only tests/scenarios/concurrent-2.json && \
    ./tools/irrlicht-replay --validate-only tests/scenarios/concurrent-4.json && \
    ./tools/irrlicht-replay --validate-only tests/scenarios/concurrent-8.json
"; then
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
