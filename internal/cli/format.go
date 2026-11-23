package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// formatCount writes n with thousands separators, such as 2,469.
func formatCount(n int) string {
	digits := strconv.Itoa(n)
	var formatted strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			formatted.WriteByte(',')
		}
		formatted.WriteRune(digit)
	}
	return formatted.String()
}

// countOf returns n and noun, in the plural unless n is 1.
func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return formatCount(n) + " " + noun + "s"
}

func formatBytes(size int64) string {
	if size < 1000 {
		return fmt.Sprintf("%d B", size)
	}
	value, unit := float64(size)/1000, "kB"
	for _, larger := range []string{"MB", "GB"} {
		if value < 1000 {
			break
		}
		value, unit = value/1000, larger
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

// timeAgo describes how long ago moment was, such as "4 minutes ago".
func timeAgo(moment time.Time) string {
	elapsed := time.Since(moment)
	switch {
	case elapsed < 5*time.Second:
		return "just now"
	case elapsed < time.Minute:
		return countOf(int(elapsed.Seconds()), "second") + " ago"
	case elapsed < time.Hour:
		return countOf(int(elapsed.Minutes()), "minute") + " ago"
	case elapsed < 24*time.Hour:
		return countOf(int(elapsed.Hours()), "hour") + " ago"
	case elapsed < 30*24*time.Hour:
		return countOf(int(elapsed.Hours()/24), "day") + " ago"
	default:
		return moment.Format("Jan 2, 2006")
	}
}

// displayPath shortens path for reading, with ~ for the home directory.
func displayPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			if rel == "." {
				return "~"
			}
			return printable("~/" + rel)
		}
	}
	return printable(path)
}
