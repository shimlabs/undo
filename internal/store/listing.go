package store

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"

	"github.com/shimlabs/undo/internal/tree"
)

// errNoBase is returned when a store has no base yet, before its first
// checkpoint.
var errNoBase = fmt.Errorf("no checkpoints yet: %w", fs.ErrNotExist)

// baseListing returns the listing of what the base holds.
func (s *Store) baseListing() (tree.Listing, error) {
	var listing tree.Listing
	err := s.readGob(s.baseListingPath(), &listing)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoBase
	}
	if err != nil {
		return nil, err
	}
	return listing, checkLocal(slices.Collect(maps.Keys(listing)))
}

// maxListingChain is how many checkpoints the saved listing may fall behind
// before it is saved again. Building forward from it reads one manifest per
// checkpoint, while saving it writes the whole listing.
const maxListingChain = 100

// ListingAt returns the project listing at checkpoint id. It starts from the
// saved listing when that is at id or at an earlier checkpoint whose later
// layers all still exist, and otherwise rebuilds the listing from the base
// and the layers.
func (s *Store) ListingAt(id int) (tree.Listing, error) {
	savedID, listing, err := s.savedListing()
	if err == nil && savedID == id {
		return listing, nil
	}
	if err == nil && savedID < id {
		if listing, err := s.listingForward(savedID, listing, id); err == nil {
			return listing, nil
		}
	}
	state, err := s.State(id)
	if err != nil {
		return nil, err
	}
	listing = make(tree.Listing, len(state))
	for rel, source := range state {
		listing[rel] = source.Entry
	}
	return listing, nil
}

// listingForward applies the layers after checkpoint from to listing, which
// is the listing at from, up to checkpoint to.
func (s *Store) listingForward(from int, listing tree.Listing, to int) (tree.Listing, error) {
	if _, err := s.lstat(s.mergePath()); err == nil {
		return nil, ErrMergePending
	}
	checkpoints, err := s.List()
	if err != nil {
		return nil, err
	}
	var later []int
	for _, checkpoint := range checkpoints {
		if checkpoint.ID > from && checkpoint.ID <= to {
			later = append(later, checkpoint.ID)
		}
	}
	if len(later) != to-from {
		return nil, fmt.Errorf("checkpoints %d to %d were merged", from+1, to)
	}
	for _, id := range later {
		manifest, err := s.Manifest(id)
		if err != nil {
			return nil, err
		}
		for _, rel := range manifest.Deleted {
			delete(listing, rel)
		}
		maps.Copy(listing, manifest.Changed)
	}
	return listing, nil
}

// SaveListing saves listing as the project's listing at checkpoint id, so
// ListingAt does not have to rebuild it from the base.
func (s *Store) SaveListing(id int, listing tree.Listing) error {
	var buffer bytes.Buffer
	encoder := gob.NewEncoder(&buffer)
	if err := encoder.Encode(id); err != nil {
		return err
	}
	if err := encoder.Encode(listing); err != nil {
		return err
	}
	return s.writeFileAtomic(s.listingPath(), buffer.Bytes())
}

// IsListingBehind reports whether the saved listing is missing or more than
// maxListingChain checkpoints before id, so it is worth saving again.
func (s *Store) IsListingBehind(id int) bool {
	savedID, err := s.savedListingID()
	return err != nil || id-savedID >= maxListingChain
}

// savedListingID returns the checkpoint the saved listing belongs to. The ID
// is encoded first, so this does not decode the listing.
func (s *Store) savedListingID() (int, error) {
	file, err := s.files.Open(s.storeName(s.listingPath()))
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var id int
	err = gob.NewDecoder(bufio.NewReader(file)).Decode(&id)
	return id, err
}

func (s *Store) savedListing() (int, tree.Listing, error) {
	data, err := s.readFile(s.listingPath())
	if err != nil {
		return 0, nil, err
	}
	decoder := gob.NewDecoder(bytes.NewReader(data))
	var id int
	var listing tree.Listing
	if err := decoder.Decode(&id); err != nil {
		return 0, nil, err
	}
	if err := decoder.Decode(&listing); err != nil {
		return 0, nil, err
	}
	return id, listing, checkLocal(slices.Collect(maps.Keys(listing)))
}
