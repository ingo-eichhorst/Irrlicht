//go:build linux

package processlifecycle

import (
	"fmt"
	"os"
	"strings"
)

// readProcessEnv reads /proc/<pid>/environ and returns the whitelisted
// entries. The file contains NUL-delimited KEY=VALUE entries.
func readProcessEnv(pid int) (map[string]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, fmt.Errorf("read /proc/%d/environ: %w", pid, err)
	}
	out := map[string]string{}
	for _, entry := range strings.Split(string(data), "\x00") {
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 {
			continue
		}
		key := entry[:eq]
		if _, ok := launcherEnvKeys[key]; !ok {
			continue
		}
		out[key] = entry[eq+1:]
	}
	return out, nil
}
