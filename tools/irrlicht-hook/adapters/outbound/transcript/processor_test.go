package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcessIncremental(t *testing.T) {
	// Create a temporary transcript file
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	// Create test transcript content with token information
	transcriptData := []string{
		`{"timestamp": "2025-01-10T10:00:01.000Z", "type": "user", "message": {"role": "user", "content": "Hello", "usage": {"input_tokens": 10, "output_tokens": 0}}}`,
		`{"timestamp": "2025-01-10T10:00:02.000Z", "type": "assistant", "message": {"role": "assistant", "content": "Hi there!", "usage": {"input_tokens": 20, "output_tokens": 15}}}`,
		`{"timestamp": "2025-01-10T10:00:03.000Z", "type": "user", "message": {"role": "user", "content": "How are you?", "usage": {"input_tokens": 30, "output_tokens": 0}}}`,
		`{"timestamp": "2025-01-10T10:00:04.000Z", "type": "assistant", "message": {"role": "assistant", "content": "I'm doing well!", "usage": {"input_tokens": 50, "output_tokens": 20}}}`,
	}

	// Write initial content
	initialContent := transcriptData[0] + "\n" + transcriptData[1] + "\n"
	err := os.WriteFile(transcriptPath, []byte(initialContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write transcript file: %v", err)
	}

	processor := NewProcessor()

	// First processing - should count tokens from first two lines
	metrics1, err := processor.ProcessIncremental(transcriptPath, 0, 0)
	if err != nil {
		t.Fatalf("First processing failed: %v", err)
	}

	// With new context size tracking: MaxContextSize should be 20 (from assistant message input)
	// TotalTokens should also be 20 since that's the true context size
	expectedTokens1 := int64(20)
	expectedMaxContext1 := int64(20)
	if metrics1.TotalTokens != expectedTokens1 {
		t.Errorf("First processing: expected %d tokens, got %d", expectedTokens1, metrics1.TotalTokens)
	}
	if metrics1.MaxContextSize != expectedMaxContext1 {
		t.Errorf("First processing: expected %d max context size, got %d", expectedMaxContext1, metrics1.MaxContextSize)
	}

	// Store the offset for next processing
	firstOffset := metrics1.NewOffset

	// Add user message (third message)
	additionalContent := transcriptData[2] + "\n"
	file, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("Failed to open transcript file for append: %v", err)
	}
	_, err = file.WriteString(additionalContent)
	file.Close()
	if err != nil {
		t.Fatalf("Failed to append to transcript file: %v", err)
	}

	// Second processing - should process user message (no context size change)
	metrics2, err := processor.ProcessIncremental(transcriptPath, firstOffset, metrics1.TotalTokens)
	if err != nil {
		t.Fatalf("Second processing failed: %v", err)
	}

	// After processing user message, MaxContextSize should still be 20
	// TotalTokens should still be 20 since no new assistant message updated the context size
	expectedTokens2 := int64(20)
	expectedMaxContext2 := int64(20)
	if metrics2.TotalTokens != expectedTokens2 {
		t.Errorf("Second processing: expected %d tokens, got %d", expectedTokens2, metrics2.TotalTokens)
	}
	if metrics2.MaxContextSize != expectedMaxContext2 {
		t.Errorf("Second processing: expected %d max context size, got %d", expectedMaxContext2, metrics2.MaxContextSize)
	}

	// Add assistant message (fourth message) - this should update context size
	assistantContent := transcriptData[3] + "\n"
	file, err = os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("Failed to open transcript file for append: %v", err)
	}
	_, err = file.WriteString(assistantContent)
	file.Close()
	if err != nil {
		t.Fatalf("Failed to append assistant content to transcript file: %v", err)
	}

	// Third processing - should update context size to 50 from assistant message
	metrics3, err := processor.ProcessIncremental(transcriptPath, metrics2.NewOffset, metrics2.TotalTokens)
	if err != nil {
		t.Fatalf("Third processing failed: %v", err)
	}

	// Now MaxContextSize should be 50 (from assistant message input tokens)
	// TotalTokens should be 50 (the true context size)
	expectedTokens3 := int64(50)
	expectedMaxContext3 := int64(50)
	if metrics3.TotalTokens != expectedTokens3 {
		t.Errorf("Third processing: expected %d tokens, got %d", expectedTokens3, metrics3.TotalTokens)
	}
	if metrics3.MaxContextSize != expectedMaxContext3 {
		t.Errorf("Third processing: expected %d max context size, got %d", expectedMaxContext3, metrics3.MaxContextSize)
	}

	// Fourth processing from same offset should return same total (no new content)
	metrics4, err := processor.ProcessIncremental(transcriptPath, metrics3.NewOffset, metrics3.TotalTokens)
	if err != nil {
		t.Fatalf("Fourth processing failed: %v", err)
	}

	if metrics4.TotalTokens != expectedTokens3 {
		t.Errorf("Fourth processing: expected %d tokens, got %d", expectedTokens3, metrics4.TotalTokens)
	}
}

func TestProcessIncrementalFileRotation(t *testing.T) {
	// Create a temporary transcript file
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	// Create larger initial content to ensure rotation detection
	initialContent := `{"timestamp": "2025-01-10T10:00:01.000Z", "type": "user", "message": {"role": "user", "content": "Hello this is a longer message to make the file larger"}}` + "\n"
	initialContent += `{"timestamp": "2025-01-10T10:00:02.000Z", "type": "assistant", "message": {"role": "assistant", "content": "Hi there! This is a long response to make the file even larger", "usage": {"input_tokens": 25, "output_tokens": 30}}}` + "\n"
	
	err := os.WriteFile(transcriptPath, []byte(initialContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write transcript file: %v", err)
	}

	processor := NewProcessor()

	// First processing
	metrics1, err := processor.ProcessIncremental(transcriptPath, 0, 0)
	if err != nil {
		t.Fatalf("First processing failed: %v", err)
	}

	// With new context tracking: MaxContextSize should be 25 (from assistant message input)
	// TotalTokens should be 25 since that's the true context size  
	expectedTokens1 := int64(25)
	expectedMaxContext1 := int64(25)
	if metrics1.TotalTokens != expectedTokens1 {
		t.Errorf("First processing: expected %d tokens, got %d", expectedTokens1, metrics1.TotalTokens)
	}
	if metrics1.MaxContextSize != expectedMaxContext1 {
		t.Errorf("First processing: expected %d max context size, got %d", expectedMaxContext1, metrics1.MaxContextSize)
	}

	// Simulate file rotation - replace with much smaller file
	newContent := `{"timestamp": "2025-01-10T11:00:01.000Z", "type": "user", "message": {"role": "user", "content": "New"}}` + "\n"
	err = os.WriteFile(transcriptPath, []byte(newContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write new transcript content: %v", err)
	}

	// Now test with the old offset (should be beyond new file size)
	t.Logf("Old offset: %d, new file size: %d", metrics1.NewOffset, len(newContent))
	metrics2, err := processor.ProcessIncremental(transcriptPath, metrics1.NewOffset, 0)
	if err != nil {
		t.Fatalf("Second processing after rotation failed: %v", err)
	}

	// After rotation, only a user message exists, so no context size should be established yet
	// TotalTokens should be 0 since no assistant message has set the context size
	expectedTokens2 := int64(0)
	expectedMaxContext2 := int64(0)
	if metrics2.TotalTokens != expectedTokens2 {
		t.Errorf("After rotation: expected %d tokens, got %d", expectedTokens2, metrics2.TotalTokens)
	}
	if metrics2.MaxContextSize != expectedMaxContext2 {
		t.Errorf("After rotation: expected %d max context size, got %d", expectedMaxContext2, metrics2.MaxContextSize)
	}
}

func TestCalculateTranscriptChecksum(t *testing.T) {
	// Create a temporary transcript file
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	// Create test content
	content := `{"timestamp": "2025-01-10T10:00:01.000Z", "type": "user", "message": "Hello"}` + "\n"
	err := os.WriteFile(transcriptPath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to write transcript file: %v", err)
	}

	processor := NewProcessor()

	// Calculate checksum
	checksum1, err := processor.CalculateTranscriptChecksum(transcriptPath)
	if err != nil {
		t.Fatalf("Failed to calculate checksum: %v", err)
	}

	if checksum1 == "" {
		t.Error("Checksum should not be empty")
	}

	// Calculate checksum again - should be the same
	checksum2, err := processor.CalculateTranscriptChecksum(transcriptPath)
	if err != nil {
		t.Fatalf("Failed to calculate checksum second time: %v", err)
	}

	if checksum1 != checksum2 {
		t.Errorf("Checksums should be identical: %s != %s", checksum1, checksum2)
	}

	// Change content
	newContent := `{"timestamp": "2025-01-10T10:00:02.000Z", "type": "assistant", "message": "Hi!"}` + "\n"
	err = os.WriteFile(transcriptPath, []byte(newContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write new content: %v", err)
	}

	// Calculate checksum for changed content
	checksum3, err := processor.CalculateTranscriptChecksum(transcriptPath)
	if err != nil {
		t.Fatalf("Failed to calculate checksum for changed content: %v", err)
	}

	if checksum1 == checksum3 {
		t.Error("Checksums should be different after content change")
	}
}

func TestContextSizeTracking(t *testing.T) {
	// Test that context size properly tracks maximum context from assistant messages
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	processor := NewProcessor()

	// Track token progression with new context size logic
	var cumulativeTokens int64 = 0
	var currentOffset int64 = 0

	// Add entries one by one and verify context size tracking
	entries := []struct {
		content           string
		expectedContext   int64
		expectedTotal     int64
		description       string
	}{
		{`{"timestamp": "2025-01-10T10:00:01.000Z", "type": "user", "message": {"role": "user", "content": "Hello"}}`, 0, 0, "user message - no context established"},
		{`{"timestamp": "2025-01-10T10:00:02.000Z", "type": "assistant", "message": {"role": "assistant", "content": "Hi!", "usage": {"input_tokens": 15, "output_tokens": 25}}}`, 15, 15, "first assistant message - establishes context size 15"},
		{`{"timestamp": "2025-01-10T10:00:03.000Z", "type": "user", "message": {"role": "user", "content": "How are you?"}}`, 15, 15, "user message - context size unchanged"},
		{`{"timestamp": "2025-01-10T10:00:04.000Z", "type": "assistant", "message": {"role": "assistant", "content": "Great!", "usage": {"input_tokens": 50, "output_tokens": 35}}}`, 50, 50, "second assistant message - updates context size to 50"},
	}

	for i, entry := range entries {
		// Add entry to file
		var content string
		if i == 0 {
			content = entry.content + "\n"
			err := os.WriteFile(transcriptPath, []byte(content), 0644)
			if err != nil {
				t.Fatalf("Failed to write initial content: %v", err)
			}
		} else {
			file, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0644)
			if err != nil {
				t.Fatalf("Failed to open file for append: %v", err)
			}
			_, err = file.WriteString(entry.content + "\n")
			file.Close()
			if err != nil {
				t.Fatalf("Failed to append entry %d: %v", i, err)
			}
		}

		// Process incrementally
		metrics, err := processor.ProcessIncremental(transcriptPath, currentOffset, cumulativeTokens)
		if err != nil {
			t.Fatalf("Processing entry %d failed: %v", i, err)
		}

		// Update cumulative count (this is now mostly for tracking offset)
		cumulativeTokens = metrics.TotalTokens

		// Verify context size matches expected
		if metrics.MaxContextSize != entry.expectedContext {
			t.Errorf("Entry %d (%s): expected context size %d, got %d", i, entry.description, entry.expectedContext, metrics.MaxContextSize)
		}

		// Verify total tokens matches expected
		if metrics.TotalTokens != entry.expectedTotal {
			t.Errorf("Entry %d (%s): expected total tokens %d, got %d", i, entry.description, entry.expectedTotal, metrics.TotalTokens)
		}

		// Verify context size never decreases (should only increase or stay same)
		if i > 0 && metrics.MaxContextSize < entries[i-1].expectedContext {
			t.Errorf("Entry %d: context size decreased from %d to %d", i, entries[i-1].expectedContext, metrics.MaxContextSize)
		}

		currentOffset = metrics.NewOffset
	}
}