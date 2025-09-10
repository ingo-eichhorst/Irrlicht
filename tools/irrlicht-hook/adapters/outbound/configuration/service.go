package configuration

import (
	"encoding/json"
	"os"
	"path/filepath"

	"irrlicht/hook/ports/outbound"
)

const (
	AppSupportDir = "Library/Application Support/Irrlicht"
)

// ServiceAdapter implements the ConfigurationService port
type ServiceAdapter struct{}

// NewServiceAdapter creates a new configuration service adapter
func NewServiceAdapter() outbound.ConfigurationService {
	return &ServiceAdapter{}
}

// IsDisabled checks if the irrlicht hook is disabled via settings or environment
func (cs *ServiceAdapter) IsDisabled() bool {
	// Check environment variable first
	if os.Getenv("IRRLICHT_DISABLED") == "1" {
		return true
	}

	// Check settings file
	return cs.isDisabledInSettings()
}

// isDisabledInSettings checks if Irrlicht is disabled in Claude settings
func (cs *ServiceAdapter) isDisabledInSettings() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false // Settings file doesn't exist or can't be read
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false // Invalid JSON
	}

	// Check hooks.irrlicht.disabled
	if hooks, ok := settings["hooks"].(map[string]interface{}); ok {
		if irrlicht, ok := hooks["irrlicht"].(map[string]interface{}); ok {
			if disabled, ok := irrlicht["disabled"].(bool); ok && disabled {
				return true
			}
		}
	}

	return false
}

// GetUserHomeDir returns the user's home directory
func (cs *ServiceAdapter) GetUserHomeDir() (string, error) {
	return os.UserHomeDir()
}

// GetInstancesDir returns the directory where session instances are stored
func (cs *ServiceAdapter) GetInstancesDir() string {
	// Check for test override
	if testDir := os.Getenv("IRRLICHT_TEST_DIR"); testDir != "" {
		return filepath.Join(testDir, "instances")
	}
	
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, AppSupportDir, "instances")
}