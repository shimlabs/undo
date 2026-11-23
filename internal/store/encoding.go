package store

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"os"

	"github.com/shimlabs/undo/internal/atomicfile"
)

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func readGob(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return gob.NewDecoder(bytes.NewReader(data)).Decode(value)
}

func writeGob(path string, value any) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(value); err != nil {
		return err
	}
	return writeFileAtomic(path, buf.Bytes())
}

func writeFileAtomic(path string, data []byte) error {
	return atomicfile.WriteFile(path, data, 0o600)
}
