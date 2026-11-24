package store

import (
	"strings"
	"testing"
)

func TestSameBytes(t *testing.T) {
	long := strings.Repeat("x", 3*contentChunkSize+7)
	tests := []struct {
		first, second string
		want          bool
	}{
		{"", "", true},
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "abcd", false},
		{"abcd", "abc", false},
		{long, long, true},
		{long, long[:len(long)-1] + "y", false},
		{long, long + "z", false},
	}
	for _, test := range tests {
		got, err := sameBytes(strings.NewReader(test.first), strings.NewReader(test.second), make([]byte, contentChunkSize), make([]byte, contentChunkSize))
		if err != nil || got != test.want {
			t.Errorf("sameBytes(%d bytes, %d bytes) = %v, %v; want %v", len(test.first), len(test.second), got, err, test.want)
		}
	}
}
