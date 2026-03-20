package capacity

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed model-capacity.json
var defaultConfigData []byte

// NewCapacityManagerFromData creates a CapacityManager from raw JSON data
// instead of reading from a file path.
func NewCapacityManagerFromData(data []byte) (*CapacityManager, error) {
	var config CapacityConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse capacity config data: %w", err)
	}

	return &CapacityManager{
		config: &config,
	}, nil
}

// DefaultCapacityManager returns a CapacityManager initialized with the
// embedded model-capacity.json. Returns nil if parsing fails.
func DefaultCapacityManager() *CapacityManager {
	cm, err := NewCapacityManagerFromData(defaultConfigData)
	if err != nil {
		return nil
	}
	return cm
}
