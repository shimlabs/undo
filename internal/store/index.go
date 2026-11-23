package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/shimlabs/undo/internal/atomicfile"
)

// The index holds one JSON line per checkpoint. Taking a checkpoint appends a
// line, so its cost does not grow with the history, and only merging old
// checkpoints rewrites the file.

// List returns the checkpoints in the order they were taken.
func (s *Store) List() ([]Checkpoint, error) {
	data, err := os.ReadFile(s.indexPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseIndex(data)
}

// Checkpoint returns checkpoint id.
func (s *Store) Checkpoint(id int) (Checkpoint, error) {
	list, err := s.List()
	if err != nil {
		return Checkpoint{}, err
	}
	i := slices.IndexFunc(list, func(cp Checkpoint) bool { return cp.ID == id })
	if i < 0 {
		return Checkpoint{}, fmt.Errorf("no checkpoint %d", id)
	}
	return list[i], nil
}

// Latest returns the most recent checkpoint, and false if there is none. It
// reads only the end of the index.
func (s *Store) Latest() (Checkpoint, bool, error) {
	file, err := os.Open(s.indexPath())
	if errors.Is(err, fs.ErrNotExist) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Checkpoint{}, false, err
	}
	const tailSize = 16 << 10
	offset := max(info.Size()-tailSize, 0)
	tail := make([]byte, info.Size()-offset)
	if _, err := file.ReadAt(tail, offset); err != nil && !errors.Is(err, io.EOF) {
		return Checkpoint{}, false, err
	}
	if offset > 0 {
		_, tail, _ = bytes.Cut(tail, []byte("\n"))
	}
	list, err := parseIndex(tail)
	if err != nil || len(list) == 0 {
		return Checkpoint{}, false, err
	}
	return list[len(list)-1], true, nil
}

// parseIndex parses index lines. A last line without a newline was cut off by
// a crash while it was written, and is ignored.
func parseIndex(data []byte) ([]Checkpoint, error) {
	var list []Checkpoint
	for len(data) > 0 {
		line, rest, isComplete := bytes.Cut(data, []byte("\n"))
		if !isComplete {
			break
		}
		data = rest
		var cp Checkpoint
		if err := json.Unmarshal(line, &cp); err != nil {
			return nil, err
		}
		list = append(list, cp)
	}
	return list, nil
}

func (s *Store) appendIndex(cp Checkpoint) error {
	line, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.indexPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(line, '\n'))
	if err == nil {
		err = atomicfile.Sync(file)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (s *Store) writeIndex(list []Checkpoint) error {
	var buf bytes.Buffer
	for _, cp := range list {
		line, err := json.Marshal(cp)
		if err != nil {
			return err
		}
		buf.Write(append(line, '\n'))
	}
	return writeFileAtomic(s.indexPath(), buf.Bytes())
}

func (s *Store) indexPath() string {
	return filepath.Join(s.dir, "index.jsonl")
}
