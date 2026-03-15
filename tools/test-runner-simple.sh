#!/bin/bash

# Simple test runner for Phase 0 components
set -e

echo "🧪 Running Phase 0 Test Suite"
echo "================================"

failed_tests=0
total_tests=0

# Test 1: Fixture validation
echo ""
echo "Test 1: Fixture validation"
echo "──────────────────────────────────────────────────"
((total_tests++))
if ./tools/irrlicht-replay --validate-only fixtures/session-start.json; then
    echo "✅ Fixture Validation: PASSED"
else
    echo "❌ Fixture Validation: FAILED"
    ((failed_tests++))
fi

# Test 2: Edge case validation (should fail)
echo ""
echo "Test 2: Edge case validation (should reject malformed JSON)"
echo "──────────────────────────────────────────────────"
((total_tests++))
if ./tools/irrlicht-replay --validate-only fixtures/edge-cases/malformed-json.txt; then
    echo "❌ Edge Case Validation: FAILED (should have rejected malformed JSON)"
    ((failed_tests++))
else
    echo "✅ Edge Case Validation: PASSED (correctly rejected malformed JSON)"
fi

# Test 3: Settings merger unit tests
echo ""
echo "Test 3: Settings merger unit tests"
echo "──────────────────────────────────────────────────"
((total_tests++))
if (cd tools/settings-merger && go test -v); then
    echo "✅ Settings Merger Unit Tests: PASSED"
else
    echo "❌ Settings Merger Unit Tests: FAILED"
    ((failed_tests++))
fi

# Test 4: Hook receiver integration
echo ""
echo "Test 4: Hook receiver integration test"
echo "──────────────────────────────────────────────────"
((total_tests++))
if ./tools/irrlicht-replay --hook-binary ./core/irrlicht-hook fixtures/session-start.json; then
    echo "✅ Hook Receiver Integration: PASSED"
else
    echo "❌ Hook Receiver Integration: FAILED"
    ((failed_tests++))
fi

# Test 5: Concurrency scenario validation
echo ""
echo "Test 5: Concurrency scenario validation"
echo "──────────────────────────────────────────────────"
((total_tests++))
if python3 -c "
import json
import sys

def validate_scenario(path):
    try:
        with open(path) as f:
            scenario = json.load(f)
        
        # Check required fields
        if 'events' not in scenario:
            print(f'❌ {path}: Missing events field')
            return False
        
        # Check each event has required fields
        for i, event in enumerate(scenario['events']):
            if 'hook_event_name' not in event:
                print(f'❌ {path}: Event {i+1} missing hook_event_name')
                return False
            if 'session_id' not in event:
                print(f'❌ {path}: Event {i+1} missing session_id')
                return False
        
        print(f'✓ {path}: Valid scenario with {len(scenario[\"events\"])} events')
        return True
    except Exception as e:
        print(f'❌ {path}: {str(e)}')
        return False

success = True
success &= validate_scenario('tests/scenarios/concurrent-2.json')
success &= validate_scenario('tests/scenarios/concurrent-4.json') 
success &= validate_scenario('tests/scenarios/concurrent-8.json')
sys.exit(0 if success else 1)
"; then
    echo "✅ Concurrent Scenarios Validation: PASSED"
else
    echo "❌ Concurrent Scenarios Validation: FAILED"
    ((failed_tests++))
fi

# Test 6: Kill switch environment variable
echo ""
echo "Test 6: Kill switch (environment variable)"
echo "──────────────────────────────────────────────────"
((total_tests++))
if IRRLICHT_DISABLED=1 ./tools/irrlicht-replay --hook-binary ./core/irrlicht-hook fixtures/session-start.json >/dev/null 2>&1; then
    echo "✅ Kill Switch (Environment): PASSED"
else
    echo "❌ Kill Switch (Environment): FAILED"
    ((failed_tests++))
fi

# Test 7: Settings merger dry run
echo ""
echo "Test 7: Settings merger dry run"
echo "──────────────────────────────────────────────────"
((total_tests++))
if (cd tools/settings-merger && ./settings-merger --dry-run --action preview >/dev/null 2>&1); then
    echo "✅ Settings Merger Dry Run: PASSED"
else
    echo "❌ Settings Merger Dry Run: FAILED"
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