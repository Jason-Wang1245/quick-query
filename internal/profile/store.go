package profile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PasswordMode string

const (
	Prompt   PasswordMode = "prompt"
	Keychain PasswordMode = "keychain"
)

type Profile struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Host         string       `json:"host"`
	Port         int          `json:"port"`
	Database     string       `json:"database"`
	User         string       `json:"user"`
	SSLMode      string       `json:"ssl_mode"`
	PasswordMode PasswordMode `json:"password_mode"`
}

func New() Profile {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return Profile{ID: hex.EncodeToString(id[:]), Host: "localhost", Port: 5432, SSLMode: "prefer", PasswordMode: Prompt}
}

func (p Profile) Validate() error {
	if p.ID == "" || strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Host) == "" || strings.TrimSpace(p.Database) == "" || strings.TrimSpace(p.User) == "" {
		return errors.New("name, host, database, and username are required")
	}
	if p.Port < 1 || p.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	switch p.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return errors.New("SSL mode: disable, allow, prefer, require, verify-ca, or verify-full")
	}
	if p.PasswordMode != Prompt && p.PasswordMode != Keychain {
		return errors.New("invalid password storage mode")
	}
	return nil
}

type Repository interface {
	Load() ([]Profile, error)
	Save([]Profile) error
}

type FileStore struct{ Path string }

func DefaultStore() (FileStore, error) {
	dir, err := os.UserConfigDir()
	return FileStore{Path: filepath.Join(dir, "quick-query", "connections.json")}, err
}

type document struct {
	Version     int       `json:"version"`
	Connections []Profile `json:"connections"`
}

func (s FileStore) Load() ([]Profile, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read connection profiles: %w", err)
	}
	var d document
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, errors.New("connection configuration is invalid JSON; repair it before saving profiles")
	}
	if d.Version != 1 {
		return nil, fmt.Errorf("unsupported connection configuration version %d", d.Version)
	}
	if err := validateProfiles(d.Connections); err != nil {
		return nil, err
	}
	return d.Connections, nil
}

func validateProfiles(profiles []Profile) error {
	seen := make(map[string]bool)
	for _, p := range profiles {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("invalid connection profile: %w", err)
		}
		if seen[p.ID] {
			return errors.New("duplicate connection profile ID")
		}
		seen[p.ID] = true
	}
	return nil
}

func (s FileStore) Save(profiles []Profile) error {
	if err := validateProfiles(profiles); err != nil {
		return err
	}
	b, err := json.MarshalIndent(document{Version: 1, Connections: profiles}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".connections-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), s.Path); err != nil {
		return err
	}
	return nil
}
