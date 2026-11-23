package tree

import (
	"cmp"
	"slices"
	"strings"
)

// SortParentsFirst sorts paths so every directory comes before what is in
// it. Paths of the same depth are sorted by name, so the paths of one
// directory come one after another and can share one open directory.
func SortParentsFirst(paths []string) {
	slices.SortFunc(paths, func(a, b string) int { return cmp.Or(Depth(a)-Depth(b), strings.Compare(a, b)) })
}

// SortDeepestFirst sorts paths so everything in a directory comes before
// the directory itself.
func SortDeepestFirst(paths []string) {
	slices.SortFunc(paths, func(a, b string) int { return cmp.Or(Depth(b)-Depth(a), strings.Compare(a, b)) })
}

// Depth returns how many directories deep rel is below the root, with -1 for
// the root itself, so sorting by depth puts parents before their children.
func Depth(rel string) int {
	if rel == "." {
		return -1
	}
	return strings.Count(rel, "/")
}
