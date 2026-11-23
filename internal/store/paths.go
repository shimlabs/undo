package store

import (
	"path/filepath"
	"strconv"
)

// The store's folder holds these files. Changing any of them needs a new
// format.
const (
	configFile         = "config.json"
	indexFile          = "index.jsonl"
	lockFile           = "lock"
	mergeFile          = "merge.json"
	logFile            = "errors.log"
	pruneMarkerFile    = "last-prune"
	baseListingFile    = "base.gob"
	listingFile        = "listing.gob"
	manifestFile       = "manifest.json"
	baseDirName        = "base"
	layersDirName      = "layers"
	markersDirName     = "markers"
	watcherFailureFile = "watcher-failure"
)

// MarkerDir is where the watcher writes the marker files it waits for.
func (s *Store) MarkerDir() string {
	return filepath.Join(s.dir, markersDirName)
}

// WatcherFailurePath is where the watcher client records why the watcher
// last failed to start.
func (s *Store) WatcherFailurePath() string {
	return filepath.Join(s.dir, watcherFailureFile)
}

func (s *Store) configPath() string {
	return filepath.Join(s.dir, configFile)
}

func (s *Store) indexPath() string {
	return filepath.Join(s.dir, indexFile)
}

func (s *Store) lockPath() string {
	return filepath.Join(s.dir, lockFile)
}

func (s *Store) mergePath() string {
	return filepath.Join(s.dir, mergeFile)
}

func (s *Store) logPath() string {
	return filepath.Join(s.dir, logFile)
}

func (s *Store) pruneMarkerPath() string {
	return filepath.Join(s.dir, pruneMarkerFile)
}

func (s *Store) baseListingPath() string {
	return filepath.Join(s.dir, baseListingFile)
}

func (s *Store) listingPath() string {
	return filepath.Join(s.dir, listingFile)
}

func (s *Store) baseDir() string {
	return filepath.Join(s.dir, baseDirName)
}

func (s *Store) basePath(rel string) string {
	if rel == "." {
		return s.baseDir()
	}
	return filepath.Join(s.baseDir(), filepath.FromSlash(rel))
}

func (s *Store) layersDir() string {
	return filepath.Join(s.dir, layersDirName)
}

func (s *Store) layerDir(id int) string {
	return filepath.Join(s.layersDir(), strconv.Itoa(id))
}

func (s *Store) layerFile(id int, rel string) string {
	return filepath.Join(s.layerDir(id), "files", filepath.FromSlash(rel))
}

func (s *Store) manifestPath(id int) string {
	return filepath.Join(s.layerDir(id), manifestFile)
}
