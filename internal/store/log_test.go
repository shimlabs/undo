package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogErrorStaysSmall(t *testing.T) {
	projectStore, err := Create(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectStore.logPath(), []byte(strings.Repeat("old failure\n", maxLogSize/12+1)), 0o600); err != nil {
		t.Fatal(err)
	}

	projectStore.LogError(errors.New("disk full"))

	info, err := os.Stat(filepath.Join(projectStore.Dir(), "errors.log"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxLogSize/2 {
		t.Fatalf("log is %d bytes, want it trimmed", info.Size())
	}
	if !strings.HasSuffix(projectStore.LastError(), " disk full") {
		t.Fatalf("last error = %q", projectStore.LastError())
	}
}
