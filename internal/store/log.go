package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LogError records a failure that happened where nobody could see it, such as
// in an agent hook or the background watcher.
func (s *Store) LogError(failure error) error {
	file, err := os.OpenFile(s.logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = fmt.Fprintf(file, "%s %v\n", time.Now().Format(time.RFC3339), failure)
	return err
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
