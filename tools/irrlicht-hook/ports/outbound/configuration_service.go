package outbound

// ConfigurationService defines the contract for configuration management
type ConfigurationService interface {
	// IsDisabled checks if the irrlicht hook is disabled via settings or environment
	IsDisabled() bool
	
	// GetUserHomeDir returns the user's home directory
	GetUserHomeDir() (string, error)
	
	// GetInstancesDir returns the directory where session instances are stored
	GetInstancesDir() string
}