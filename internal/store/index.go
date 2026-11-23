package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"

	"github.com/shimlabs/undo/internal/atomicfile"
	"golang.org/x/sys/unix"
)

// The index holds one JSON line per checkpoint. Taking a checkpoint appends a
// line, so its cost does not grow with the history, and only merging old
// checkpoints rewrites the file.

// List returns the checkpoints in the order they were taken.
func (s *Store) List() ([]Checkpoint, error) {
	data, err := s.readFile(s.indexPath())
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
	checkpoints, err := s.List()
	if err != nil {
		return Checkpoint{}, err
	}
	i := slices.IndexFunc(checkpoints, func(checkpoint Checkpoint) bool { return checkpoint.ID == id })
	if i < 0 {
		return Checkpoint{}, fmt.Errorf("%w %d", ErrNoCheckpoint, id)
	}
	return checkpoints[i], nil
}

// Latest returns the most recent checkpoint, and false if there is none. It
// reads only the end of the index.
func (s *Store) Latest() (Checkpoint, bool, error) {
	file, err := s.open(s.indexPath())
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
	checkpoints, err := parseIndex(tail)
	if err != nil || len(checkpoints) == 0 {
		return Checkpoint{}, false, err
	}
	return checkpoints[len(checkpoints)-1], true, nil
}

// parseIndex parses index lines. A last line without a newline was cut off by
// a crash while it was written, and is ignored.
func parseIndex(data []byte) ([]Checkpoint, error) {
	var checkpoints []Checkpoint
	for len(data) > 0 {
		line, rest, isComplete := bytes.Cut(data, []byte("\n"))
		if !isComplete {
			break
		}
		data = rest
		var checkpoint Checkpoint
		if err := json.Unmarshal(line, &checkpoint); err != nil {
			return nil, err
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	return checkpoints, nil
}

// appendIndex adds checkpoint to the index. A line cut off by a crash is
// removed first, so the new line does not join it.
func (s *Store) appendIndex(checkpoint Checkpoint) error {
	line, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	file, err := s.openFile(s.indexPath(), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW)
	if err != nil {
		return err
	}
	end, err := completeLinesEnd(file)
	if err == nil {
		err = file.Truncate(end)
	}
	if err == nil {
		_, err = file.WriteAt(append(line, '\n'), end)
	}
	if err == nil {
		err = atomicfile.Sync(file)
	}
	return errors.Join(err, file.Close())
}

// completeLinesEnd returns the size of index file without a last line that
// has no newline.
func completeLinesEnd(file *os.File) (int64, error) {
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return 0, err
	}
	last := make([]byte, 1)
	if _, err := file.ReadAt(last, info.Size()-1); err != nil {
		return 0, err
	}
	if last[0] == '\n' {
		return info.Size(), nil
	}
	data := make([]byte, info.Size())
	if _, err := file.ReadAt(data, 0); err != nil {
		return 0, err
	}
	return int64(bytes.LastIndexByte(data, '\n') + 1), nil
}

func (s *Store) writeIndex(checkpoints []Checkpoint) error {
	var buffer bytes.Buffer
	for _, checkpoint := range checkpoints {
		line, err := json.Marshal(checkpoint)
		if err != nil {
			return err
		}
		buffer.Write(append(line, '\n'))
	}
	return s.writeFileAtomic(s.indexPath(), buffer.Bytes())
}
