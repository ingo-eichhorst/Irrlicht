package filesystem

import (
	"fmt"
	"os"
	"time"
)

// writeFileAtomic writes data to path via a temp file + rename so a reader
// can never observe a torn write. The temp name carries pid+nanotime —
// matching the repository and cost-tracker writers — so two processes
// (e.g. two daemons mistakenly sharing one IRRLICHT_HOME) can't clobber
// each other's in-flight temp file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
