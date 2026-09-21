package accountquota

import (
	"context"
	"fmt"
	"os"
	"strings"

	outbound "irrlicht/core/ports/outbound"
)

// FileCredentialResolver resolves a credential from a plain file — one of
// the two axes issue #2003 §1.3 requires kept separate from the transport and
// from a provider's choice of authentication method. A later provider ticket
// supplies Path; this package owns only the read.
type FileCredentialResolver struct {
	// Path resolves the absolute path to read, honoring whatever env
	// override the provider's own CLI honors — the same contract
	// agent.ManagedUserFile.Path documents.
	Path func() (string, error)
}

// Resolve implements outbound.CredentialResolver. The file's trimmed content
// is the whole credential; a provider whose file carries structure (JSON,
// TOML) extracts the field itself and wraps the extracted string in
// outbound.NewCredential, never passing the raw file through this resolver.
func (r FileCredentialResolver) Resolve(_ context.Context) (outbound.Credential, error) {
	if r.Path == nil {
		return outbound.Credential{}, fmt.Errorf("accountquota: FileCredentialResolver has no Path resolver")
	}
	path, err := r.Path()
	if err != nil {
		return outbound.Credential{}, fmt.Errorf("accountquota: resolving credential file path: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// os.ReadFile's error embeds the path, never file content — safe to
		// wrap directly. A credential file's CONTENT must never reach this
		// far; only the read failing does.
		return outbound.Credential{}, fmt.Errorf("accountquota: reading credential file: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return outbound.Credential{}, fmt.Errorf("accountquota: credential file %q is empty", path)
	}
	return outbound.NewCredential(secret), nil
}
