package store

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
)

func (s *Store) readJSON(path string, value any) error {
	data, err := s.readFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func (s *Store) writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.writeFileAtomic(path, data)
}

func (s *Store) readGob(path string, value any) error {
	data, err := s.readFile(path)
	if err != nil {
		return err
	}
	return gob.NewDecoder(bytes.NewReader(data)).Decode(value)
}

func (s *Store) writeGob(path string, value any) error {
	var buffer bytes.Buffer
	if err := gob.NewEncoder(&buffer).Encode(value); err != nil {
		return err
	}
	return s.writeFileAtomic(path, buffer.Bytes())
}
