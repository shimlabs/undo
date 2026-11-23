package store

// toSet returns paths as a set.
func toSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, rel := range paths {
		set[rel] = true
	}
	return set
}
