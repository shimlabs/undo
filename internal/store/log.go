package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxLogSize bounds the error log. A failure that repeats on every command
// would otherwise grow it without limit.
const maxLogSize = 1 << 20

// LogError records a failure that happened where nobody could see it, such as
// in an agent hook or the background watcher. Once the log passes maxLogSize,
// it starts over.
// Logging is best effort: if the log cannot be written, the failure is lost.
func (s *Store) LogError(failure error) {
	flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY
	if info, err := os.Stat(s.logPath()); err == nil && info.Size() > maxLogSize {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(s.logPath(), flags, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "%s %v\n", time.Now().Format(time.RFC3339), failure)
}

// LastError returns the most recent line written by LogError, or "" if there
// is none.
func (s *Store) LastError() string {
	data, err := os.ReadFile(s.logPath())
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return lines[len(lines)-1]
}

func (s *Store) logPath() string {
	return filepath.Join(s.dir, "errors.log")
}
