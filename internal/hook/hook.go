// Package hook connects undo to coding agents.
package hook

import (
	"encoding/json"
	"io"
	"os"

	"github.com/shimlabs/undo/internal/project"
	"github.com/shimlabs/undo/internal/store"
)

// preToolUseInput is the part of a PreToolUse event that undo reads. Claude Code and
// Codex send the same fields.
type preToolUseInput struct {
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

// handlePreToolUse reads one PreToolUse event from in and takes a checkpoint labeled by
// label. Errors are logged in the store, and the agent is never blocked.
func handlePreToolUse(in io.Reader, checkpoint store.Checkpointer, label func(event preToolUseInput, root string) string) error {
	var event preToolUseInput
	if err := json.NewDecoder(in).Decode(&event); err != nil {
		return err
	}
	dir := event.Cwd
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	root, err := project.Root(dir)
	if err != nil {
		return err
	}
	s, err := store.Open(root)
	if err != nil {
		return err
	}
	if _, err := checkpoint(s, label(event, root), dir); err != nil {
		s.LogError(err)
		return err
	}
	return nil
}
