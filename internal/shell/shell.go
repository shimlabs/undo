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

// Join returns words as a shell command line, quoting only the words that
// need it.
func Join(words []string) string {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = word
		if word == "" || strings.ContainsFunc(word, needsQuote) {
			quoted[i] = Quote(word)
		}
	}
	return strings.Join(quoted, " ")
}

// needsQuote reports whether r means something to a shell, or is not plain
// text, in a word.
func needsQuote(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("@%+=:,./_-", r)
}
