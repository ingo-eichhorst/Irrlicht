package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <command> [args...]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  capacity <model-name>           - Get capacity info for model\n")
		fmt.Fprintf(os.Stderr, "  estimate <model-name> <text>    - Estimate tokens from text\n")
		fmt.Fprintf(os.Stderr, "  utilization <model-name> <tokens> - Calculate context utilization\n")
		fmt.Fprintf(os.Stderr, "  list                           - List all known models\n")
		os.Exit(1)
	}
	
	// Find config file
	configPath := "../model-capacity.json"
	if absPath, err := filepath.Abs(configPath); err == nil {
		configPath = absPath
	}
	
	cm, err := NewCapacityManager(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	
	command := os.Args[1]
	
	switch command {
	case "capacity":
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: %s capacity <model-name>\n", os.Args[0])
			os.Exit(1)
		}
		handleCapacity(cm, os.Args[2])
		
	case "estimate":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: %s estimate <model-name> <text>\n", os.Args[0])
			os.Exit(1)
		}
		handleEstimate(cm, os.Args[2], os.Args[3])
		
	case "utilization":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Usage: %s utilization <model-name> <token-count>\n", os.Args[0])
			os.Exit(1)
		}
		handleUtilization(cm, os.Args[2], os.Args[3])
		
	case "list":
		handleList(cm)
		
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		os.Exit(1)
	}
}

func handleCapacity(cm *CapacityManager, modelName string) {
	capacity := cm.GetModelCapacity(modelName)
	output, err := json.MarshalIndent(capacity, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling capacity: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(output))
}

func handleEstimate(cm *CapacityManager, modelName, text string) {
	estimation := cm.EstimateTokensFromContent(text, modelName)
	output, err := json.MarshalIndent(estimation, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling estimation: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(output))
}

func handleUtilization(cm *CapacityManager, modelName, tokenStr string) {
	var tokens int64
	if _, err := fmt.Sscanf(tokenStr, "%d", &tokens); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid token count: %s\n", tokenStr)
		os.Exit(1)
	}
	
	utilization := cm.CalculateContextUtilization(tokens, modelName, false)
	output, err := json.MarshalIndent(utilization, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling utilization: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(output))
}

func handleList(cm *CapacityManager) {
	cm.mu.RLock()
	models := make([]string, 0, len(cm.config.Models))
	for modelName, capacity := range cm.config.Models {
		models = append(models, fmt.Sprintf("%-20s %s (%dK tokens)", 
			modelName, capacity.DisplayName, capacity.ContextWindow/1000))
	}
	cm.mu.RUnlock()
	
	fmt.Println("Available Models:")
	for _, model := range models {
		fmt.Printf("  %s\n", model)
	}
}