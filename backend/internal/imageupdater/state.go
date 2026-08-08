package imageupdater

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type State struct {
	Version         string    `json:"version"`
	PreviousVersion string    `json:"previous_version"`
	ImageID         string    `json:"image_id"`
	LastError       string    `json:"last_error,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type StateStore struct {
	path string
}

func NewStateStore(path string) *StateStore {
	return &StateStore{path: filepath.Clean(path)}
}

func (s *StateStore) Save(state State) error {
	if _, err := NormalizeVersion(state.Version); err != nil {
		return err
	}
	if _, err := NormalizeVersion(state.PreviousVersion); err != nil {
		return err
	}
	if !strings.HasPrefix(state.ImageID, "sha256:") {
		return fmt.Errorf("invalid image ID")
	}
	state.UpdatedAt = time.Now().UTC()
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWriteFile(s.path, body, 0600)
}

func (s *StateStore) Load() (State, error) {
	body, err := os.ReadFile(s.path)
	if err != nil {
		return State{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var state State
	if err := decoder.Decode(&state); err != nil {
		return State{}, err
	}
	if _, err := NormalizeVersion(state.Version); err != nil {
		return State{}, err
	}
	if _, err := NormalizeVersion(state.PreviousVersion); err != nil {
		return State{}, err
	}
	if !strings.HasPrefix(state.ImageID, "sha256:") {
		return State{}, fmt.Errorf("invalid image ID")
	}
	return state, nil
}

func (s *StateStore) Clear() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
