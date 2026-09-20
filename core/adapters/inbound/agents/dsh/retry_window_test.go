package dsh

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/pkg/tailer"
)

func TestRetryErrorSurvivesRetryStartedUntilSuccessfulBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.v3.jsonl.zstd")
	writeZstdFrame(t, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, `{"type":"session","version":3,"createdAt":1789781348000}`+"\n"+measuredRetryRecord+"\n")
	tailer := newTestTranscriptTailer(path)

	metrics := tailTranscript(t, tailer)
	assertRetryingServerError(t, metrics)

	writeZstdFrame(t, path, os.O_WRONLY|os.O_APPEND, measuredRetryStartedRecord+"\n")
	metrics = tailTranscript(t, tailer)
	assertRetryingServerError(t, metrics)

	writeZstdFrame(t, path, os.O_WRONLY|os.O_APPEND, `{"type":"turn/end","time":1789781353000,"data":{"turn":1,"reason":{"kind":"completed"}}}`+"\n")
	metrics = tailTranscript(t, tailer)
	if metrics.SessionError != nil {
		t.Fatalf("successful turn/end retained retry error: %+v", metrics.SessionError)
	}
}

func TestMalformedRetryFailsLoudly(t *testing.T) {
	retry := parseRecord(t, &Parser{}, `{"type":"llm/retry","data":{"retry":1,"maxRetries":5,"delayMs":10,"failure":{"message":"missing code"}}}`)
	if retry.SessionError == nil || retry.SessionError.Class != "malformed_retry_record" {
		t.Fatalf("malformed retry = %+v, want malformed_retry_record", retry)
	}
}

func assertRetryingServerError(t *testing.T, metrics *tailer.SessionMetrics) {
	t.Helper()
	if metrics.SessionError == nil || metrics.SessionError.Phase != tailer.ErrorPhaseRetrying || metrics.SessionError.Class != "server" {
		t.Fatalf("session error = %+v, want retrying server error", metrics.SessionError)
	}
}

const measuredRetryRecord = `{"type":"llm/retry","seq":16,"time":1789781348757,"data":{"retryId":"9162aaae-dba6-4059-8a73-f0873c456c87","turn":1,"step":1,"retry":1,"maxRetries":5,"delayMs":453.45541167226247,"failure":{"message":"529: provider overloaded","code":"SERVER"}}}`

const measuredRetryStartedRecord = `{"type":"llm/retry-started","seq":18,"time":1789781349212,"data":{"retryId":"9162aaae-dba6-4059-8a73-f0873c456c87","turn":1,"step":1,"retry":1}}`
