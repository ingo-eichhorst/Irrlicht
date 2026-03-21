// Package config defines daemon-wide configuration.
package config

import "time"

// DefaultMaxSessionAge is the default maximum age for sessions. Sessions
// whose transcript files have not been modified within this window are not
// loaded on startup and are silently dropped by the file-system watcher.
const DefaultMaxSessionAge = 5 * 24 * time.Hour

// Config holds daemon-wide runtime configuration.
type Config struct {
	MaxSessionAge time.Duration
}

// Default returns a Config populated with production defaults.
func Default() Config {
	return Config{MaxSessionAge: DefaultMaxSessionAge}
}
