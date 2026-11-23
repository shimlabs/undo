package project

// canonicalCase returns path unchanged: Linux filesystems are case-sensitive,
// so there is only one spelling.
func canonicalCase(path string) (string, error) {
	return path, nil
}
