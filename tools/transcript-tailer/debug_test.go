package main

import (
	"fmt"
	"log"
	"os"
	"transcript-tailer/pkg/tailer"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: transcript-tailer <transcript-path>")
	}
	
	transcriptPath := os.Args[1]
	fmt.Printf("Testing transcript tailer on: %s\n", transcriptPath)
	
	t := tailer.NewTranscriptTailer(transcriptPath)
	metrics, err := t.TailAndProcess()
	if err != nil {
		log.Fatalf("Error processing transcript: %v", err)
	}
	
	if metrics == nil {
		fmt.Println("No metrics returned")
		return
	}
	
	fmt.Printf("Messages per minute: %.2f\n", metrics.MessagesPerMinute)
	fmt.Printf("Elapsed seconds: %d\n", metrics.ElapsedSeconds)
	fmt.Printf("Last message at: %v\n", metrics.LastMessageAt)
	fmt.Printf("Session start at: %v\n", metrics.SessionStartAt)
	fmt.Printf("Model name: %s\n", metrics.ModelName)
	fmt.Printf("Total tokens: %d\n", metrics.TotalTokens)
}