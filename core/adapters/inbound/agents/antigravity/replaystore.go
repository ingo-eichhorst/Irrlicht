package antigravity

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// StageReplayStore implements tailer.ReplayStoreStager (issue #766). Live, the
// parser locates the conversation usage store by climbing
// brain/<conv>/.system_generated/logs/ to <root> and reading
// <root>/conversations/<conv>.db (see storePathForTranscript in dbmetrics.go).
// During replay the recorded transcript is materialized in a flat scratch dir,
// so the recording rig captures the store under
// recordingDir/store/conversations/<conv>.db and this hook rebuilds the live
// layout under tmpDir: it copies the store to tmpDir/conversations/ and returns
// a transcript path nested under tmpDir/brain/<conv>/.system_generated/logs/, so
// the unchanged storePathForTranscript resolves the staged store exactly as it
// does live.
//
// When no store was captured (every recording made before #766, and every
// non-Antigravity adapter) it returns "" so the replay engine falls back to the
// flat transcript path — reproducing a pre-#719 storeless session byte-for-byte.
func (p *Parser) StageReplayStore(tmpDir, recordingDir string) (string, error) {
	srcDir := filepath.Join(recordingDir, "store", "conversations")
	dbs, _ := filepath.Glob(filepath.Join(srcDir, "*.db"))
	if len(dbs) == 0 {
		return "", nil
	}
	// The conv-id is the join key shared by the brain transcript dir and the
	// store filename, so reuse the captured store's <conv>.db name for both.
	conv := strings.TrimSuffix(filepath.Base(dbs[0]), ".db")

	// Mirror the live store location: tmpDir/conversations/<conv>.db plus any
	// -wal/-shm. Copy every file in the captured store dir verbatim so the
	// WAL-aware reader sees the same bytes it would live.
	if err := copyDirFiles(srcDir, filepath.Join(tmpDir, "conversations")); err != nil {
		return "", err
	}

	// Recreate the brain transcript tree so sessionIDFromPath/storePathForTranscript
	// climb back to tmpDir and find conversations/<conv>.db. The engine opens the
	// returned path to write the transcript bytes, so we make only its parent.
	logsDir := filepath.Join(tmpDir, "brain", conv, ".system_generated", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(logsDir, transcriptFilename), nil
}

// copyDirFiles copies every regular file directly under src into dst (created
// if needed). Non-recursive: the conversation store dir holds only the .db and
// its -wal/-shm siblings.
func copyDirFiles(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
