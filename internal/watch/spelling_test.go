package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/shimlabs/undo/internal/tree"
)

// BenchmarkRemoveStaleSpellings measures the check after 10,000 new files
// spread over many folders, as a branch switch or an unzip makes.
func BenchmarkRemoveStaleSpellings(b *testing.B) {
	for _, dirCount := range []int{200, 2000} {
		b.Run(strconv.Itoa(dirCount), func(b *testing.B) {
			root := b.TempDir()
			listing := tree.Listing{".": {Kind: tree.Dir}}
			for i := range dirCount {
				dir := fmt.Sprintf("d%04d", i)
				if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
					b.Fatal(err)
				}
				listing[dir] = tree.Entry{Kind: tree.Dir}
			}
			newFiles := make([]string, 0, 10000)
			for i := range 10000 {
				rel := fmt.Sprintf("d%04d/new%05d", i%dirCount, i)
				if err := os.WriteFile(filepath.Join(root, rel), nil, 0o600); err != nil {
					b.Fatal(err)
				}
				newFiles = append(newFiles, rel)
			}
			projectTracker := &tracker{root: root, listing: listing, isCaseInsensitive: true}
			for b.Loop() {
				changes := newChangeSet(listing)
				for _, rel := range newFiles {
					changes.changed[rel] = true
					listing[rel] = tree.Entry{Kind: tree.File}
					changes.uncertainDirs[filepath.Dir(rel)] = true
				}
				parents := newParentDirs(root)
				projectTracker.removeStaleSpellings(parents, changes)
				parents.close()
			}
		})
	}
}
