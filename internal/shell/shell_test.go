package shell

import "testing"

func TestQuoteRoundTrips(t *testing.T) {
	for _, text := range []string{"/usr/local/bin/undo", "it's $(app)", "a b", ""} {
		quoted := Quote(text)
		if got, ok := Unquote(quoted); !ok || got != text {
			t.Errorf("Unquote(Quote(%q)) = %q, %v", text, got, ok)
		}
	}
	if _, ok := Unquote("undo"); ok {
		t.Error("Unquote accepted a word without quotes")
	}
}
