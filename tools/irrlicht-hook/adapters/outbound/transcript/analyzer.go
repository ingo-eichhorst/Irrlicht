package transcript

import (
	"time"

	"irrlicht/hook/domain/metrics"
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

	// Use processor to analyze the transcript
	tailerResult, err := a.processor.TailAndProcess(transcriptPath)
	if err != nil || tailerResult == nil {
		return nil, err
	}

	// Convert tailer metrics to domain metrics format
	hookMetrics := &metrics.SessionMetrics{
		ElapsedSeconds:     tailerResult.ElapsedSeconds,
		TotalTokens:        tailerResult.TotalTokens,
		ModelName:          tailerResult.ModelName,
		ContextUtilization: tailerResult.ContextUtilization,
		PressureLevel:      tailerResult.PressureLevel,
	}

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