#!/bin/bash

# Test runner for Phase 0 components
set -e

echo "🧪 Running Phase 0 Test Suite"
echo "================================"

# Function to run tests and capture results
run_test() {
    local test_name="$1"
    local command="$2"
    
    echo ""
    echo "Running $test_name..."
    echo "$(printf '─%.0s' {1..50})"
    
    if eval "$command"; then
        echo "✅ $test_name: PASSED"
        return 0
    else
        echo "❌ $test_name: FAILED"
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
    echo "✅ Edge Case Validation: PASSED (correctly rejected malformed JSON)"
else
    echo "❌ Edge Case Validation: FAILED (should have rejected malformed JSON)"
    ((failed_tests++))
fi

# Test 3: Settings merger unit tests
((total_tests++))
if ! run_test "Settings Merger Unit Tests" "cd tools/settings-merger && go test -v"; then
    ((failed_tests++))
fi

# Test 4: Hook receiver integration test
((total_tests++))
if ! run_test "Hook Receiver Integration" "./tools/irrlicht-replay --hook-binary ./tools/irrlicht-hook/irrlicht-hook fixtures/session-start.json"; then
    ((failed_tests++))
fi

# Test 5: Settings merger build test
((total_tests++))
if ! run_test "Settings Merger Build" "cd tools/settings-merger && go build -o settings-merger ."; then
    ((failed_tests++))
fi

# Test 6: Concurrency scenario validation
((total_tests++))
if ! run_test "Concurrent Scenarios Validation" "
    ./tools/irrlicht-replay --validate-only tests/scenarios/concurrent-2.json && \
    ./tools/irrlicht-replay --validate-only tests/scenarios/concurrent-4.json && \
    ./tools/irrlicht-replay --validate-only tests/scenarios/concurrent-8.json
"; then
    ((failed_tests++))
fi

# Test 7: Kill switch environment variable
((total_tests++))
if ! run_test "Kill Switch (Environment)" "
    IRRLICHT_DISABLED=1 ./tools/irrlicht-replay --hook-binary ./tools/irrlicht-hook/irrlicht-hook fixtures/session-start.json >/dev/null 2>&1; \
    test \$? -eq 0  # Should exit successfully with status 0
"; then
    ((failed_tests++))
fi

# Summary
echo ""
echo "🏁 Test Summary"
echo "==============="
echo "Total tests: $total_tests"
echo "Passed: $((total_tests - failed_tests))"
echo "Failed: $failed_tests"

if [ $failed_tests -eq 0 ]; then
    echo ""
    echo "🎉 All tests passed! Phase 0 implementation is ready."
    exit 0
else
    echo ""
    echo "⚠️  Some tests failed. Please check the output above."
    exit 1
fi