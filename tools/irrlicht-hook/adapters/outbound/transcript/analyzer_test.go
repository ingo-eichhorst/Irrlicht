package transcript

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/hook/domain/session"
	"irrlicht/hook/ports/outbound"
)

func TestAnalyzerIncremental(t *testing.T) {
	// Create a temporary transcript file
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	// Create test session
	sess := session.NewSession("test-session", session.Working)
	sess.TranscriptPath = transcriptPath

	// Create analyzer
	config := outbound.DefaultTranscriptConfig()
	analyzer := NewAnalyzer(config)

	// Create initial transcript content - user messages don't have usage tokens in real Claude Code
	initialContent := `{"timestamp": "2025-01-10T10:00:01.000Z", "type": "user", "message": {"role": "user", "content": "Hello"}}` + "\n"
	err := os.WriteFile(transcriptPath, []byte(initialContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write initial transcript: %v", err)
	}

	// First incremental processing
	metrics1, err := analyzer.ComputeSessionMetricsIncremental(sess)
	if err != nil {
		t.Fatalf("First incremental processing failed: %v", err)
	}

	// Verify first processing - user message with no context established should be 0
	if metrics1.TotalTokens != 0 {
		t.Errorf("First processing: expected 0 tokens, got %d", metrics1.TotalTokens)
	}

	// Verify processing state was updated
	if sess.ProcessingState == nil {
		t.Fatal("Processing state should have been created")
	}

	if sess.ProcessingState.CumulativeTokens != 0 {
		t.Errorf("Processing state tokens: expected 0, got %d", sess.ProcessingState.CumulativeTokens)
	}

	if sess.ProcessingState.LastProcessedOffset == 0 {
		t.Error("Processing state offset should be > 0")
	}

	// Store the state for comparison
	firstOffset := sess.ProcessingState.LastProcessedOffset

	// Add more content to transcript
	additionalContent := `{"timestamp": "2025-01-10T10:00:02.000Z", "type": "assistant", "message": {"role": "assistant", "content": "Hi there!", "usage": {"input_tokens": 20, "output_tokens": 25}}}` + "\n"
	file, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("Failed to open transcript for append: %v", err)
	}
	_, err = file.WriteString(additionalContent)
	file.Close()
	if err != nil {
		t.Fatalf("Failed to append to transcript: %v", err)
	}

	// Second incremental processing
	metrics2, err := analyzer.ComputeSessionMetricsIncremental(sess)
	if err != nil {
		t.Fatalf("Second incremental processing failed: %v", err)
	}

	// Verify cumulative token count - assistant message establishes context size of 20
	expectedTokens := int64(20) // context size from assistant input tokens
	if metrics2.TotalTokens != expectedTokens {
		t.Errorf("Second processing: expected %d tokens, got %d", expectedTokens, metrics2.TotalTokens)
	}

	// Verify processing state was updated
	if sess.ProcessingState.CumulativeTokens != expectedTokens {
		t.Errorf("Processing state cumulative tokens: expected %d, got %d", expectedTokens, sess.ProcessingState.CumulativeTokens)
	}

	if sess.ProcessingState.LastProcessedOffset <= firstOffset {
		t.Error("Processing offset should have increased")
	}

	// Note: Checksum might change when appending if it affects the first 1KB
	// This is acceptable behavior for rotation detection

	// Third processing without new content - should return same total
	metrics3, err := analyzer.ComputeSessionMetricsIncremental(sess)
	if err != nil {
		t.Fatalf("Third incremental processing failed: %v", err)
	}

	if metrics3.TotalTokens != expectedTokens {
		t.Errorf("Third processing: expected %d tokens, got %d", expectedTokens, metrics3.TotalTokens)
	}
}

func TestAnalyzerIncrementalRotation(t *testing.T) {
	// Create a temporary transcript file
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	// Create test session
	sess := session.NewSession("test-session", session.Working)
	sess.TranscriptPath = transcriptPath

	// Create analyzer
	config := outbound.DefaultTranscriptConfig()
	analyzer := NewAnalyzer(config)

	// Create initial transcript content - user messages don't have usage tokens in real Claude Code
	initialContent := `{"timestamp": "2025-01-10T10:00:01.000Z", "type": "user", "message": {"role": "user", "content": "Hello"}}` + "\n"
	initialContent += `{"timestamp": "2025-01-10T10:00:02.000Z", "type": "assistant", "message": {"role": "assistant", "content": "Hi!", "usage": {"input_tokens": 50, "output_tokens": 25}}}` + "\n"
	
	err := os.WriteFile(transcriptPath, []byte(initialContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write initial transcript: %v", err)
	}

	// First processing
	metrics1, err := analyzer.ComputeSessionMetricsIncremental(sess)
	if err != nil {
		t.Fatalf("First processing failed: %v", err)
	}

	expectedTokens1 := int64(50) // context size from assistant input tokens
	if metrics1.TotalTokens != expectedTokens1 {
		t.Errorf("First processing: expected %d tokens, got %d", expectedTokens1, metrics1.TotalTokens)
	}

	// Store original checksum
	originalChecksum := sess.ProcessingState.TranscriptChecksum

	// Simulate transcript rotation - completely replace content (user message without usage)
	newContent := `{"timestamp": "2025-01-10T11:00:01.000Z", "type": "user", "message": {"role": "user", "content": "New session"}}` + "\n"
	err = os.WriteFile(transcriptPath, []byte(newContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write rotated transcript: %v", err)
	}

	// Process after rotation
	metrics2, err := analyzer.ComputeSessionMetricsIncremental(sess)
	if err != nil {
		t.Fatalf("Processing after rotation failed: %v", err)
	}

	// Should detect rotation and reset - user message only, no context established
	expectedTokens2 := int64(0)
	if metrics2.TotalTokens != expectedTokens2 {
		t.Errorf("After rotation: expected %d tokens, got %d", expectedTokens2, metrics2.TotalTokens)
	}

	// Verify processing state was reset
	if sess.ProcessingState.CumulativeTokens != expectedTokens2 {
		t.Errorf("Processing state after rotation: expected %d tokens, got %d", expectedTokens2, sess.ProcessingState.CumulativeTokens)
	}

	// Checksum should be different
	if sess.ProcessingState.TranscriptChecksum == originalChecksum {
		t.Error("Checksum should change after rotation")
	}

	// Offset should be reset and then updated
	if sess.ProcessingState.LastProcessedOffset == 0 {
		t.Error("Offset should be updated after processing new content")
	}
}

func TestAnalyzerContextSizeNeverDecrease(t *testing.T) {
	// Test to ensure context size never decreases during normal operation
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

	sess := session.NewSession("test-session", session.Working)
	sess.TranscriptPath = transcriptPath

	config := outbound.DefaultTranscriptConfig()
	analyzer := NewAnalyzer(config)

	var lastTokenCount int64 = 0

	// Add entries incrementally and verify context size tracking
	entries := []struct {
		content           string
		expectedTokens    int64
		description       string
	}{
		{`{"timestamp": "2025-01-10T10:00:01.000Z", "type": "user", "message": {"role": "user", "content": "Hi"}}`, 0, "user message - no context established"},
		{`{"timestamp": "2025-01-10T10:00:02.000Z", "type": "assistant", "message": {"role": "assistant", "content": "Hello!", "usage": {"input_tokens": 15, "output_tokens": 20}}}`, 15, "assistant message establishes context size 15"},
		{`{"timestamp": "2025-01-10T10:00:03.000Z", "type": "user", "message": {"role": "user", "content": "How are you?"}}`, 15, "user message - context size unchanged"},
		{`{"timestamp": "2025-01-10T10:00:04.000Z", "type": "assistant", "message": {"role": "assistant", "content": "I'm doing well, thanks for asking!", "usage": {"input_tokens": 30, "output_tokens": 40}}}`, 30, "assistant message updates context size to 30"},
	}

	for i, entry := range entries {
		// Add entry to transcript
		if i == 0 {
			err := os.WriteFile(transcriptPath, []byte(entry.content+"\n"), 0644)
			if err != nil {
				t.Fatalf("Failed to write entry %d: %v", i, err)
			}
		} else {
			file, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0644)
			if err != nil {
				t.Fatalf("Failed to open transcript for entry %d: %v", i, err)
			}
			_, err = file.WriteString(entry.content + "\n")
			file.Close()
			if err != nil {
				t.Fatalf("Failed to append entry %d: %v", i, err)
			}
		}

		// Process incrementally
		metrics, err := analyzer.ComputeSessionMetricsIncremental(sess)
		if err != nil {
			t.Fatalf("Processing entry %d failed: %v", i, err)
		}

		// Verify context size never decreases
		if metrics.TotalTokens < lastTokenCount {
			t.Errorf("Entry %d (%s): context size decreased from %d to %d", i, entry.description, lastTokenCount, metrics.TotalTokens)
		}

		// Verify expected token count
		if metrics.TotalTokens != entry.expectedTokens {
			t.Errorf("Entry %d (%s): expected %d tokens, got %d", i, entry.description, entry.expectedTokens, metrics.TotalTokens)
		}

		lastTokenCount = metrics.TotalTokens
	}
}