package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <transcript-path>\n", os.Args[0])
		os.Exit(1)
	}

	transcriptPath := os.Args[1]
	
	// Check if file exists
	if _, err := os.Stat(transcriptPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: transcript file does not exist: %s\n", transcriptPath)
		os.Exit(1)
	}

	// Create tailer and process transcript
	tailer := NewTranscriptTailer(transcriptPath)
	metrics, err := tailer.TailAndProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error processing transcript: %v\n", err)
		os.Exit(1)
	}

	// Output metrics as JSON
	output, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling metrics: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(string(output))
}