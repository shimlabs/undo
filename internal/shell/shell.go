// Package shell quotes words for a POSIX shell.
package shell

import "strings"

// Quote returns text as one single-quoted shell word.
func Quote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// Unquote returns the text of a word Quote made, and false for any other
// word.
func Unquote(word string) (string, bool) {
	if len(word) < 2 || !strings.HasPrefix(word, "'") || !strings.HasSuffix(word, "'") {
		return "", false
	}
	return strings.ReplaceAll(word[1:len(word)-1], `'\''`, "'"), true
}
