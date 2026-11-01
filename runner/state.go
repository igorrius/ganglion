package runner

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sync"
)

// State is a module's persistent key-value store, namespaced by module
// name. In P2 it moves to NATS KV (docs/spec.md, section 9.2).
type State interface {
	Get(module, key string) (string, bool)
	Put(module, key, value string)
}

// MemState is an in-memory State that can be saved to and loaded from a
// JSON file.
type MemState struct {
	mu   sync.Mutex
	data map[string]map[string]string
}

// NewMemState returns an empty store.
func NewMemState() *MemState {
	return &MemState{data: map[string]map[string]string{}}
}

// Get returns a module's value.
func (s *MemState) Get(module, key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[module][key]
	return v, ok
}

// Put sets a module's value.
func (s *MemState) Put(module, key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[module] == nil {
		s.data[module] = map[string]string{}
	}
	s.data[module][key] = value
}

// LoadMemState reads a store saved by Save; a missing file is an empty store.
func LoadMemState(path string) (*MemState, error) {
	s := NewMemState()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	return s, nil
}

// Save writes the store to path, readable by the owner only: module state
// can hold tokens.
func (s *MemState) Save(path string) error {
	s.mu.Lock()
	b, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
