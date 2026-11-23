package store

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/shimlabs/undo/internal/tree"
)

// errNoBase is returned when a store has no base yet, before its first
// checkpoint.
var errNoBase = fmt.Errorf("no checkpoints yet: %w", fs.ErrNotExist)

type savedListing struct {
	ID      int
	Listing tree.Listing
}

// BaseListing returns the listing of what the base holds.
func (s *Store) BaseListing() (tree.Listing, error) {
	var listing tree.Listing
	err := readGob(s.baseListingPath(), &listing)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoBase
	}
	return listing, err
}

// ListingAt returns the project listing at checkpoint id. It uses the saved
// copy when that matches, and otherwise rebuilds the listing from the base and
// the layers.
func (s *Store) ListingAt(id int) (tree.Listing, error) {
	var saved savedListing
	if readGob(s.listingPath(), &saved) == nil && saved.ID == id {
		return saved.Listing, nil
	}
	state, err := s.State(id)
	if err != nil {
		return nil, err
	}
	listing := make(tree.Listing, len(state))
	for rel, source := range state {
		listing[rel] = source.Entry
	}
	return listing, nil
}

func (s *Store) saveListing(id int, listing tree.Listing) error {
	return writeGob(s.listingPath(), savedListing{ID: id, Listing: listing})
}

func (s *Store) baseListingPath() string {
	return filepath.Join(s.dir, "base.gob")
}

func (s *Store) listingPath() string {
	return filepath.Join(s.dir, "listing.gob")
}
