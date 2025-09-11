package transcript

import (
	"time"

	"irrlicht/hook/domain/metrics"
	"irrlicht/hook/domain/session"
	"irrlicht/hook/ports/outbound"
)

// Analyzer implements the TranscriptAnalyzer interface
type Analyzer struct {
	config    *outbound.TranscriptConfig
	processor *Processor
}

// NewAnalyzer creates a new transcript analyzer
func NewAnalyzer(config *outbound.TranscriptConfig) *Analyzer {
	if config == nil {
		config = outbound.DefaultTranscriptConfig()
	}
	return &Analyzer{
		config:    config,
		processor: NewProcessor(),
	}
}

// AnalyzeTranscript analyzes a transcript file and returns metrics
func (a *Analyzer) AnalyzeTranscript(transcriptPath string) (*metrics.AnalysisResult, error) {
	// Use processor to analyze the transcript
	tailerResult, err := a.processor.TailAndProcess(transcriptPath)
	if err != nil || tailerResult == nil {
		return nil, err
	}

	return &metrics.AnalysisResult{
		ElapsedSeconds:     tailerResult.ElapsedSeconds,
		TotalTokens:        tailerResult.TotalTokens,
		ModelName:          tailerResult.ModelName,
		ContextUtilization: 0.0, // Not calculated in this implementation
		SessionStartTime:   &tailerResult.SessionStartAt,
		LastActivityTime:   &tailerResult.LastMessageAt,
	}, nil
}

// ComputeSessionMetrics computes session metrics from transcript
func (a *Analyzer) ComputeSessionMetrics(transcriptPath string, existingMetrics *metrics.SessionMetrics) (*metrics.SessionMetrics, error) {
	if transcriptPath == "" {
		return nil, nil
	}

	if !a.IsTranscriptValid(transcriptPath) {
		return nil, nil
	}

	// Use processor to analyze the transcript (legacy mode - full processing)
	tailerResult, err := a.processor.TailAndProcess(transcriptPath)
	if err != nil || tailerResult == nil {
		return nil, err
	}

	// Convert tailer metrics to domain metrics format
	hookMetrics := &metrics.SessionMetrics{
		ElapsedSeconds:               tailerResult.ElapsedSeconds,
		TotalTokens:                  tailerResult.TotalTokens,
		ModelName:                    tailerResult.ModelName,
		ContextUtilization:           tailerResult.ContextUtilization,
		PressureLevel:                tailerResult.PressureLevel,
		
		// ccusage-compatible consumption metrics
		CumulativeInputTokens:        tailerResult.CumulativeInputTokens,
		CumulativeOutputTokens:       tailerResult.CumulativeOutputTokens,
		CumulativeCacheCreationTokens: tailerResult.CumulativeCacheCreationTokens,
		CumulativeCacheReadTokens:     tailerResult.CumulativeCacheReadTokens,
	}
	
	// Calculate total consumption
	hookMetrics.TotalConsumptionTokens = hookMetrics.CumulativeInputTokens + hookMetrics.CumulativeOutputTokens + 
		hookMetrics.CumulativeCacheCreationTokens + hookMetrics.CumulativeCacheReadTokens

	return hookMetrics, nil
}

// ComputeSessionMetricsIncremental computes session metrics using incremental processing
func (a *Analyzer) ComputeSessionMetricsIncremental(sess *session.Session) (*metrics.SessionMetrics, error) {
	if sess.TranscriptPath == "" {
		return nil, nil
	}

	if !a.IsTranscriptValid(sess.TranscriptPath) {
		return nil, nil
	}

	// Get processing state from session
	lastOffset := int64(0)
	baseTokens := int64(0)
	var lastChecksum string

	if sess.ProcessingState != nil {
		lastOffset = sess.ProcessingState.LastProcessedOffset
		baseTokens = sess.ProcessingState.CumulativeTokens
		lastChecksum = sess.ProcessingState.TranscriptChecksum
	}

	// Check if transcript was rotated/cleared by comparing checksum
	currentChecksum, err := a.processor.CalculateTranscriptChecksum(sess.TranscriptPath)
	if err != nil {
		// Can't calculate checksum, proceed with existing state
		currentChecksum = lastChecksum
	}

	// If checksum changed, transcript was rotated - reset processing state
	if lastChecksum != "" && currentChecksum != lastChecksum {
		lastOffset = 0
		baseTokens = 0
	}

	// Use incremental processor to analyze only new content
	tailerResult, err := a.processor.ProcessIncremental(sess.TranscriptPath, lastOffset, baseTokens)
	if err != nil || tailerResult == nil {
		return nil, err
	}

	// Update session processing state
	if sess.ProcessingState == nil {
		sess.ProcessingState = &session.ProcessingState{}
	}
	sess.ProcessingState.LastProcessedOffset = tailerResult.NewOffset
	sess.ProcessingState.CumulativeTokens = tailerResult.TotalTokens
	sess.ProcessingState.LastProcessedAt = time.Now()
	sess.ProcessingState.TranscriptChecksum = currentChecksum

	// Convert tailer metrics to domain metrics format
	hookMetrics := &metrics.SessionMetrics{
		ElapsedSeconds:               tailerResult.ElapsedSeconds,
		TotalTokens:                  tailerResult.TotalTokens,
		ModelName:                    tailerResult.ModelName,
		ContextUtilization:           tailerResult.ContextUtilization,
		PressureLevel:                tailerResult.PressureLevel,
		
		// ccusage-compatible consumption metrics
		CumulativeInputTokens:        tailerResult.CumulativeInputTokens,
		CumulativeOutputTokens:       tailerResult.CumulativeOutputTokens,
		CumulativeCacheCreationTokens: tailerResult.CumulativeCacheCreationTokens,
		CumulativeCacheReadTokens:     tailerResult.CumulativeCacheReadTokens,
	}
	
	// Calculate total consumption
	hookMetrics.TotalConsumptionTokens = hookMetrics.CumulativeInputTokens + hookMetrics.CumulativeOutputTokens + 
		hookMetrics.CumulativeCacheCreationTokens + hookMetrics.CumulativeCacheReadTokens

	return hookMetrics, nil
}

// GetTranscriptSize returns the size of a transcript file
func (a *Analyzer) GetTranscriptSize(transcriptPath string) (int64, error) {
	return a.processor.GetFileSize(transcriptPath)
}

// IsTranscriptValid checks if a transcript file is valid and readable
func (a *Analyzer) IsTranscriptValid(transcriptPath string) bool {
	if transcriptPath == "" {
		return false
	}

	exists, err := a.processor.FileExists(transcriptPath)
	if err != nil || !exists {
		return false
	}

	// Check file size if configured
	if a.config.MaxFileSize > 0 {
		if size, err := a.GetTranscriptSize(transcriptPath); err == nil {
			if size > a.config.MaxFileSize {
				return false
			}
		}
	}

	return true
}

// GetLastModified returns the last modification time of a transcript
func (a *Analyzer) GetLastModified(transcriptPath string) (time.Time, error) {
	return a.processor.GetLastModified(transcriptPath)
}

// normalizeModelName normalizes model names for consistency
func (a *Analyzer) normalizeModelName(modelName string) string {
	if modelName == "" {
		return ""
	}
	
	// Simple normalization - could be enhanced later
	return modelName
}