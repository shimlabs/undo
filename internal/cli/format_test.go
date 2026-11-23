package cli

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestFormatCount(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := formatCount(n); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestPluralize(t *testing.T) {
	for n, want := range map[int]string{0: "0 paths", 1: "1 path", 2469: "2,469 paths"} {
		if got := pluralize(n, "path"); got != want {
			t.Errorf("pluralize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for size, want := range map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 2_340_000: "2.3 MB", 5_000_000_000: "5.0 GB"} {
		if got := formatBytes(size); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", size, got, want)
		}
	}
}

func TestTimeAgo(t *testing.T) {
	old := time.Date(2025, time.March, 4, 12, 0, 0, 0, time.Local)
	tests := []struct {
		moment time.Time
		want   string
	}{
		{time.Now(), "just now"},
		{time.Now().Add(-12 * time.Second), "12 seconds ago"},
		{time.Now().Add(-61 * time.Second), "1 minute ago"},
		{time.Now().Add(-3 * time.Hour), "3 hours ago"},
		{time.Now().Add(-49 * time.Hour), "2 days ago"},
		{old, "Mar 4, 2025"},
	}
	for _, test := range tests {
		if got := timeAgo(test.moment); got != test.want {
			t.Errorf("timeAgo(%v) = %q, want %q", test.moment, got, test.want)
		}
	}
}

func TestDisplayPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tests := map[string]string{
		home:                                  "~",
		filepath.Join(home, "code", "app"):    "~/code/app",
		"/srv/app":                            "/srv/app",
		filepath.Join(home, "..", "neighbor"): filepath.Join(filepath.Dir(home), "neighbor"),
	}
	for path, want := range tests {
		if got := displayPath(filepath.Clean(path)); got != want {
			t.Errorf("displayPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestGroupedRows(t *testing.T) {
	rows := groupedRows("restore", []string{".", "README.md", "src", "src/main.go", "src/util.go"})
	want := []changeRow{
		{action: "restore", path: "README.md"},
		{action: "restore", path: "src/", detail: "3 paths"},
	}
	if !slices.Equal(rows, want) {
		t.Fatalf("groupedRows = %+v, want %+v", rows, want)
	}
}
