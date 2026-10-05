package swfolder

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// StateFile records which version in Addigy the folder last matched: the
// version export wrote it from, or the version the last publish created.
// Publishing compares it to Addigy to detect changes made outside the folder
// (drift), so the folder's own changes don't count as drift.
const StateFile = ".addigyctl-state.yaml"

// State is the content of StateFile.
type State struct {
	InstructionID string `yaml:"instruction_id"`
	Version       string `yaml:"version"`
	// Checksum covers the version's content as Addigy returned it, so an edit
	// made in place in Addigy (same instruction ID) is noticed too.
	Checksum string `yaml:"checksum"`
}

const stateHeader = "# Written by addigyctl: the Addigy version this folder last matched.\n" +
	"# Commit it with the folder and don't edit it; publishing uses it to detect\n" +
	"# changes made in Addigy outside this folder.\n\n"

// WriteState writes s to dir's state file.
func WriteState(dir string, s State) error {
	var buf bytes.Buffer
	buf.WriteString(stateHeader)
	if err := yaml.NewEncoder(&buf).Encode(s); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, StateFile), buf.Bytes(), 0o644)
}

// ErrNoState is returned by ReadState when the folder has no state file.
var ErrNoState = errors.New("no " + StateFile)

// ReadState reads dir's state file.
func ReadState(dir string) (State, error) {
	var s State
	data, err := os.ReadFile(filepath.Join(dir, StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return s, ErrNoState
	}
	if err != nil {
		return s, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("%s: %w", filepath.Join(dir, StateFile), err)
	}
	if s.InstructionID == "" || s.Checksum == "" {
		return s, fmt.Errorf("%s: instruction_id and checksum are required", filepath.Join(dir, StateFile))
	}
	return s, nil
}
