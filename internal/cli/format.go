package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shimlabs/undo/internal/terminal"
)

// formatCount returns count with thousands separators, such as 2,469.
func formatCount(count int) string {
	digits := strconv.Itoa(count)
	var formatted strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			formatted.WriteByte(',')
		}
		formatted.WriteRune(digit)
	}
	return formatted.String()
}

// pluralize returns count and noun, in the plural unless count is 1.
func pluralize(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return formatCount(count) + " " + noun + "s"
}

// pluralWord returns singular when count is 1, and plural otherwise.
func pluralWord(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
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
		return pluralize(int(elapsed.Seconds()), "second") + " ago"
	case elapsed < time.Hour:
		return pluralize(int(elapsed.Minutes()), "minute") + " ago"
	case elapsed < 24*time.Hour:
		return pluralize(int(elapsed.Hours()), "hour") + " ago"
	case elapsed < 30*24*time.Hour:
		return pluralize(int(elapsed.Hours()/24), "day") + " ago"
	default:
		return moment.Format("Jan 2, 2006")
	}
}

// displayPath shortens path for reading, with ~ for the home directory.
func displayPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, path); err == nil && filepath.IsLocal(rel) {
			if rel == "." {
				return "~"
			}
			return terminal.Printable("~/" + rel)
		}
	}
	return terminal.Printable(path)
}

// joinWithAnd joins items as a list in a sentence: "a", "a and b", "a, b
// and c".
func joinWithAnd(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// displayRel returns rel safe to print, ending in "/" when it is a folder.
func displayRel(rel string, isDir bool) string {
	if isDir {
		return terminal.Printable(rel) + "/"
	}
	return terminal.Printable(rel)
}
