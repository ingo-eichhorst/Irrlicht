package relay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// daemonTokenFilename is the basename of the daemon's relay bearer token,
// stored under the daemon data dir at mode 0600. Unlike the relay server's
// tokens file (a list of hashed records named tokens.json), this holds the
// daemon's own single plaintext token — it must send it on the wire, so it
// cannot be hashed. It deliberately uses a DIFFERENT filename so that running
// both irrlichd and `irrlichtrelay` under one $IRRLICHT_HOME (the dev/test
// coexist pattern) can't make one clobber the other's incompatible JSON shape.
const daemonTokenFilename = "relay-token.json"

// daemonToken is the on-disk shape of the daemon's relay token file.
type daemonToken struct {
	Token string `json:"token"`
}

// LoadDaemonToken returns the daemon's relay bearer token: the
// IRRLICHT_RELAY_TOKEN env var if set, else the "token" field of
// <dir>/relay-token.json. Best-effort — returns "" when neither is present (a
// no-auth relay needs no token), and never errors so a missing/garbled file
// just means "no token".
func LoadDaemonToken(dir string) string {
	if v := strings.TrimSpace(os.Getenv("IRRLICHT_RELAY_TOKEN")); v != "" {
		return v
	}
	data, err := os.ReadFile(filepath.Join(dir, daemonTokenFilename))
	if err != nil {
		return ""
	}
	var t daemonToken
	if err := json.Unmarshal(data, &t); err != nil {
		return ""
	}
	return strings.TrimSpace(t.Token)
}
