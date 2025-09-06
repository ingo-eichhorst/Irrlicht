package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranscriptTailer_BasicParsing(t *testing.T) {
	// Create temporary transcript file
	tmpDir, err := os.MkdirTemp("", "transcript_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	
	// Write test transcript data
	testData := `{"timestamp": "2025-09-06T16:00:00.000Z", "event_type": "user_message", "content": "Hello"}
{"timestamp": "2025-09-06T16:00:30.000Z", "event_type": "assistant_message", "content": "Hi there"}
{"timestamp": "2025-09-06T16:01:00.000Z", "event_type": "tool_call", "tool": "bash", "args": {"command": "ls"}}
{"timestamp": "2025-09-06T16:01:15.000Z", "event_type": "tool_result", "result": "file1.txt file2.txt"}
{"timestamp": "2025-09-06T16:01:30.000Z", "event_type": "session_end"}`

	err = os.WriteFile(transcriptPath, []byte(testData), 0644)
	require.NoError(t, err)

	// Process with tailer
	tailer := NewTranscriptTailer(transcriptPath)
	tailer.windowSize = 2 * time.Minute // Extend window to include all test messages
	metrics, err := tailer.TailAndProcess()
	require.NoError(t, err)
	require.NotNil(t, metrics)

	// Verify metrics
	assert.Equal(t, 4, len(tailer.metrics.MessageHistory)) // 4 message events (session_end not counted)
	assert.True(t, metrics.MessagesPerMinute > 0)
	assert.True(t, metrics.ElapsedSeconds >= 0)
}

func TestTranscriptTailer_MessagesPerMinute(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "transcript_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	
	// Create events spanning exactly 2 minutes with 6 messages
	baseTime := time.Now().Add(-2 * time.Minute)
	events := []string{
		fmt.Sprintf(`{"timestamp": "%s", "event_type": "user_message", "content": "msg1"}`, baseTime.Format(time.RFC3339)),
		fmt.Sprintf(`{"timestamp": "%s", "event_type": "assistant_message", "content": "msg2"}`, baseTime.Add(20*time.Second).Format(time.RFC3339)),
		fmt.Sprintf(`{"timestamp": "%s", "event_type": "user_message", "content": "msg3"}`, baseTime.Add(40*time.Second).Format(time.RFC3339)),
		fmt.Sprintf(`{"timestamp": "%s", "event_type": "assistant_message", "content": "msg4"}`, baseTime.Add(60*time.Second).Format(time.RFC3339)),
		fmt.Sprintf(`{"timestamp": "%s", "event_type": "user_message", "content": "msg5"}`, baseTime.Add(80*time.Second).Format(time.RFC3339)),
		fmt.Sprintf(`{"timestamp": "%s", "event_type": "assistant_message", "content": "msg6"}`, baseTime.Add(100*time.Second).Format(time.RFC3339)),
	}

	testData := ""
	for _, event := range events {
		testData += event + "\n"
	}

	err = os.WriteFile(transcriptPath, []byte(testData), 0644)
	require.NoError(t, err)

	// Process with tailer
	tailer := NewTranscriptTailer(transcriptPath)
	
	// Set a custom window size for testing
	tailer.windowSize = 2 * time.Minute
	
	metrics, err := tailer.TailAndProcess()
	require.NoError(t, err)

	// Should be 3.6 messages per minute (6 messages over 100 seconds = 3.6/min)
	assert.InDelta(t, 3.6, metrics.MessagesPerMinute, 0.1)
}

func TestTranscriptTailer_DefensiveParsing(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "transcript_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	
	// Mix of valid and invalid JSONL
	testData := `{"timestamp": "2025-09-06T16:00:00.000Z", "event_type": "user_message", "content": "Valid message"}
{invalid json line}
{"timestamp": "2025-09-06T16:00:30.000Z", "event_type": "assistant_message", "content": "Another valid"}
incomplete line without closing brace
{"timestamp": "2025-09-06T16:01:00.000Z", "event_type": "tool_call", "content": "Valid tool call"}`

	err = os.WriteFile(transcriptPath, []byte(testData), 0644)
	require.NoError(t, err)

	// Process with tailer - should not crash on invalid lines
	tailer := NewTranscriptTailer(transcriptPath)
	metrics, err := tailer.TailAndProcess()
	require.NoError(t, err)
	require.NotNil(t, metrics)

	// Should have parsed 3 valid message events
	assert.Equal(t, 3, len(tailer.metrics.MessageHistory))
}

func TestTranscriptTailer_LargeFile64KBTail(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "transcript_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	transcriptPath := filepath.Join(tmpDir, "large_transcript.jsonl")
	
	// Create a file larger than 64KB with messages
	file, err := os.Create(transcriptPath)
	require.NoError(t, err)
	defer file.Close()

	// Write ~100KB of data with messages throughout
	baseTime := time.Now().Add(-10 * time.Minute)
	lineTemplate := `{"timestamp": "%s", "event_type": "user_message", "content": "This is a test message with some padding content to make each line longer so we can reach 64KB+ file size easily"}`
	
	totalBytes := 0
	messageCount := 0
	
	for totalBytes < 100*1024 { // 100KB
		timestamp := baseTime.Add(time.Duration(messageCount*10) * time.Second)
		line := fmt.Sprintf(lineTemplate, timestamp.Format(time.RFC3339)) + "\n"
		
		_, err := file.WriteString(line)
		require.NoError(t, err)
		
		totalBytes += len(line)
		messageCount++
	}
	
	file.Close()

	// Process with tailer - should only read last ~64KB
	tailer := NewTranscriptTailer(transcriptPath)
	metrics, err := tailer.TailAndProcess()
	require.NoError(t, err)
	require.NotNil(t, metrics)

	// Should have processed some messages, but not all (due to 64KB limit)
	assert.True(t, len(tailer.metrics.MessageHistory) > 0)
	assert.True(t, len(tailer.metrics.MessageHistory) < messageCount) // Less than total due to tailing
}

func TestTranscriptTailer_IncrementalProcessing(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "transcript_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	transcriptPath := filepath.Join(tmpDir, "incremental.jsonl")
	
	// Write initial data
	initialData := `{"timestamp": "2025-09-06T16:00:00.000Z", "event_type": "user_message", "content": "First message"}`
	err = os.WriteFile(transcriptPath, []byte(initialData+"\n"), 0644)
	require.NoError(t, err)

	// First processing
	tailer := NewTranscriptTailer(transcriptPath)
	tailer.windowSize = 2 * time.Minute // Extend window for all messages
	_, err = tailer.TailAndProcess()
	require.NoError(t, err)
	assert.Equal(t, 1, len(tailer.metrics.MessageHistory))

	// Append more data
	file, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	
	additionalData := `{"timestamp": "2025-09-06T16:00:30.000Z", "event_type": "assistant_message", "content": "Second message"}
{"timestamp": "2025-09-06T16:01:00.000Z", "event_type": "user_message", "content": "Third message"}`
	
	_, err = file.WriteString(additionalData + "\n")
	require.NoError(t, err)
	file.Close()

	// Second processing - should only process new lines
	metrics2, err := tailer.TailAndProcess()
	require.NoError(t, err)
	assert.Equal(t, 3, len(tailer.metrics.MessageHistory))
	
	// Messages per minute should be positive since we have more messages
	assert.True(t, metrics2.MessagesPerMinute > 0)
	
	// Check that more messages were processed
	assert.True(t, len(tailer.metrics.MessageHistory) > 1)
}

func TestTranscriptTailer_TokenExtraction(t *testing.T) {
	// Create temporary transcript file
	tmpDir, err := os.MkdirTemp("", "transcript_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	
	// Write sample transcript data with token information
	transcriptData := `{"timestamp": "2025-09-06T10:00:01.000Z", "event_type": "user_message", "message": "Hello", "model": "claude-3.5-sonnet"}
{"timestamp": "2025-09-06T10:00:02.000Z", "event_type": "assistant_message", "message": "Hi there!", "usage": {"input_tokens": 50, "output_tokens": 25, "total_tokens": 75}, "model": "claude-3.5-sonnet"}
{"timestamp": "2025-09-06T10:00:03.000Z", "event_type": "user_message", "message": "How are you?", "usage": {"input_tokens": 100, "output_tokens": 0, "total_tokens": 100}, "model": "claude-3.5-sonnet"}
`
	
	err = os.WriteFile(transcriptPath, []byte(transcriptData), 0644)
	require.NoError(t, err)
	
	// Create tailer
	tailer := NewTranscriptTailer(transcriptPath)
	
	// Process transcript
	metrics, err := tailer.TailAndProcess()
	require.NoError(t, err)
	
	// Check that metrics were computed
	assert.NotNil(t, metrics)
	assert.Equal(t, 3, len(tailer.metrics.MessageHistory))
	
	// Check token extraction
	assert.Equal(t, int64(100), metrics.TotalTokens) // Should be the latest/highest token count
	assert.Equal(t, "claude-3.5-sonnet", metrics.ModelName)
	
	// Check context utilization was computed
	assert.True(t, metrics.ContextUtilization >= 0)
	assert.NotEmpty(t, metrics.PressureLevel)
	// With 100 tokens out of 200K context window, should be very low utilization
	assert.True(t, metrics.ContextUtilization < 1.0)
	assert.Equal(t, "safe", metrics.PressureLevel)
}