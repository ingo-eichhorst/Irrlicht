package museaccountapi

import (
	"fmt"
	"os"
	"path/filepath"
)

// AuthPath resolves ~/.config/muse/auth.json, honoring the same env
// overrides the Muse Code launcher itself honors — order verified against
// the pinned herdr-agent-quota source's own auth_path(): $MUSE_AUTH_PATH,
// then $XDG_CONFIG_HOME/muse/auth.json, then ~/.config/muse/auth.json. Pass
// this as CredentialResolver.Path.
func AuthPath() (string, error) {
	if p := os.Getenv("MUSE_AUTH_PATH"); p != "" {
		return p, nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "muse", "auth.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("museaccountapi: resolving $HOME: %w", err)
	}
	return filepath.Join(home, ".config", "muse", "auth.json"), nil
}
