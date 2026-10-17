package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Network struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Owner   string    `json:"owner"` // Clerk user ID
	Created time.Time `json:"created"`
}

type Member struct {
	Network  string `json:"network"`
	ClerkID  string `json:"clerkID"`
	Handle   string `json:"handle"`
	HSUser   string `json:"hsUser"`   // Headscale username: <handle>-<networkID>
	HSUserID string `json:"hsUserID"` // Headscale numeric user ID
}

type Invite struct {
	Hash    string    `json:"hash"` // sha256(code), hex
	Network string    `json:"network"`
	Expires time.Time `json:"expires"`
	Used    bool      `json:"used"`
}

type State struct {
	Networks []Network `json:"networks"`
	Members  []Member  `json:"members"`
	Invites  []Invite  `json:"invites"`
}

// Store is the whole server state in one JSON file. Callers take Lock for a
// read-modify-write and call Save before Unlock.
type Store struct {
	sync.Mutex
	path string
	State
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(b, &s.State)
}

func (s *Store) Save() error {
	b, err := json.MarshalIndent(s.State, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) network(id string) *Network {
	for i := range s.Networks {
		if s.Networks[i].ID == id {
			return &s.Networks[i]
		}
	}
	return nil
}

func (s *Store) memberByHandle(network, handle string) *Member {
	for i := range s.Members {
		if m := &s.Members[i]; m.Network == network && m.Handle == handle {
			return m
		}
	}
	return nil
}

func (s *Store) memberByClerk(network, clerkID string) *Member {
	for i := range s.Members {
		if m := &s.Members[i]; m.Network == network && m.ClerkID == clerkID {
			return m
		}
	}
	return nil
}

func (s *Store) removeMember(network, handle string) {
	out := s.Members[:0]
	for _, m := range s.Members {
		if !(m.Network == network && m.Handle == handle) {
			out = append(out, m)
		}
	}
	s.Members = out
}
