package desktopdriver

// Reading the JSONL a run produces: the Claude Code transcript, and the
// daemon recording that must show the state sequence the scenario expects.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func validateNoToolTranscript(path string) error {
	found, err := jsonlContains(path, containsToolRecord)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("Desktop transcript %q contains a tool call or tool result", path)
	}
	return nil
}

func containsToolRecord(value map[string]any) bool {
	return containsToolValue(value)
}

func containsToolValue(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, child := range typed {
			if containsToolValue(child) {
				return true
			}
		}
	case map[string]any:
		if kind, ok := typed["type"].(string); ok && (kind == "tool_use" || kind == "tool_result") {
			return true
		}
		for _, child := range typed {
			if containsToolValue(child) {
				return true
			}
		}
	}
	return false
}

func jsonlContains(path string, predicate func(map[string]any) bool) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var value map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			return false, fmt.Errorf("decode recording %q: %w", path, err)
		}
		if predicate(value) {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func recordingHasStateSequence(directory, sessionID string, expected []string) (bool, error) {
	files, err := filepath.Glob(filepath.Join(directory, "*.jsonl"))
	if err != nil {
		return false, err
	}
	sort.Strings(files)
	matched := 0
	for _, file := range files {
		_, err := jsonlContains(file, func(value map[string]any) bool {
			if matched < len(expected) && value["kind"] == "state_transition" &&
				value["session_id"] == sessionID && value["new_state"] == expected[matched] {
				matched++
			}
			return matched == len(expected)
		})
		if err != nil {
			return false, err
		}
		if matched == len(expected) {
			return true, nil
		}
	}
	return false, nil
}
