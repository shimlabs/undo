package walk

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
)

// buildTree returns a directory tree as the children of each directory.
func buildTree(depth, width int) map[string][]string {
	children := map[string][]string{}
	var build func(dir string, level int)
	build = func(dir string, level int) {
		if level == depth {
			return
		}
		for i := range width {
			child := fmt.Sprintf("%s/%d", dir, i)
			children[dir] = append(children[dir], child)
			build(child, level+1)
		}
	}
	build("", 0)
	return children
}

func TestDirsVisitsEveryDirectoryOnce(t *testing.T) {
	children := buildTree(4, 5)
	var mu sync.Mutex
	var visited []string

	err := Dirs("", func(dir string) ([]string, error) {
		mu.Lock()
		visited = append(visited, dir)
		mu.Unlock()
		return children[dir], nil
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{""}
	for _, kids := range children {
		want = append(want, kids...)
	}
	slices.Sort(want)
	slices.Sort(visited)
	if !slices.Equal(visited, want) {
		t.Fatalf("visited %d directories, want %d", len(visited), len(want))
	}
}

func TestDirsReturnsFirstError(t *testing.T) {
	children := buildTree(3, 4)
	failure := errors.New("unreadable")

	err := Dirs("", func(dir string) ([]string, error) {
		if dir == "/2/1" {
			return nil, failure
		}
		return children[dir], nil
	})
	if !errors.Is(err, failure) {
		t.Fatalf("Dirs = %v, want %v", err, failure)
	}
}
